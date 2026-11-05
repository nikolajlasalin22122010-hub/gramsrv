// Command daskgram-ops is the agent a user installs on their own VDS so @ChatBot
// can report on it. The agent connects outbound only: we never dial the host and
// never store a password. Enrollment swaps a one-time code for a long-lived
// token; the token file is created 0600.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const defaultStatePath = "/etc/daskgram-ops/token"

func main() {
	var (
		server  = flag.String("server", "https://daskgram.xyz/ops", "ops endpoint base URL")
		state   = flag.String("state", defaultStatePath, "where the agent token is stored")
		enroll  = flag.Bool("enroll", false, "exchange -code for an agent token and exit")
		code    = flag.String("code", "", "one-time enrollment code from the bot")
		name    = flag.String("name", "", "label shown in the chat")
		once    = flag.Bool("once", false, "run a single poll cycle and exit")
		verbose = flag.Bool("v", false, "log every poll")
	)
	flag.Parse()

	if *enroll {
		if strings.TrimSpace(*code) == "" {
			fatal("--enroll requires --code")
		}
		if err := doEnroll(*server, *state, *code, *name); err != nil {
			fatal(err.Error())
		}
		return
	}

	token, err := os.ReadFile(*state)
	if err != nil {
		fatal("no token at " + *state + " -- run with --enroll --code <code> first")
	}
	if err := run(*server, strings.TrimSpace(string(token)), *once, *verbose); err != nil {
		fatal(err.Error())
	}
}

func doEnroll(server, state, code, name string) error {
	body, _ := json.Marshal(map[string]string{"code": strings.TrimSpace(code), "name": name})
	resp, err := http.Post(strings.TrimRight(server, "/")+"/api/enroll", "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("enroll failed: %s", strings.TrimSpace(string(raw)))
	}
	var out struct {
		Token   string `json:"token"`
		AgentID int64  `json:"agent_id"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || out.Token == "" {
		return fmt.Errorf("enroll: unexpected response")
	}
	if err := os.MkdirAll(filepath.Dir(state), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(state, []byte(out.Token), 0o600); err != nil {
		return err
	}
	fmt.Printf("enrolled as agent %d, token saved to %s\n", out.AgentID, state)
	return nil
}

func run(server, token string, once, verbose bool) error {
	client := &http.Client{Timeout: 30 * time.Second}
	for {
		job, err := poll(client, server, token)
		if err != nil {
			if verbose {
				fmt.Println("poll:", err)
			}
		} else if job != nil {
			fmt.Printf("job %d: %s\n", job.ID, job.Kind)
			output := diagnostics()
			if err := report(client, server, token, job.ID, output); err != nil {
				fmt.Println("report:", err)
			}
		} else if verbose {
			fmt.Println("poll: no jobs")
		}
		if once {
			return nil
		}
		time.Sleep(3 * time.Second)
	}
}

type job struct {
	ID   int64  `json:"job_id"`
	Kind string `json:"kind"`
}

func poll(client *http.Client, server, token string) (*job, error) {
	req, _ := http.NewRequest(http.MethodPost, strings.TrimRight(server, "/")+"/api/poll", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNoContent {
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	var out job
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return &out, nil
}

func report(client *http.Client, server, token string, jobID int64, output string) error {
	body, _ := json.Marshal(map[string]any{"job_id": jobID, "output": output})
	req, _ := http.NewRequest(http.MethodPost, strings.TrimRight(server, "/")+"/api/result", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	return nil
}

// diagnostics is a fixed set of read-only checks. Nothing here comes from the
// chat: the bot asks for a report, it does not get to name a command.
func diagnostics() string {
	var b strings.Builder
	host, _ := os.Hostname()
	b.WriteString("Сервер: " + host + "\n")
	if out, err := runCmd("uptime"); err == nil {
		b.WriteString("Аптайм и нагрузка: " + strings.TrimSpace(out) + "\n")
	}
	b.WriteString(fmt.Sprintf("CPU: %d ядер\n", runtime.NumCPU()))
	if total, avail, ok := memory(); ok {
		b.WriteString(fmt.Sprintf("Память: %s из %s занято, свободно %s\n",
			gb(total-avail), gb(total), gb(avail)))
	}
	if out, err := runCmd("df", "-h", "/"); err == nil {
		lines := strings.Split(strings.TrimSpace(out), "\n")
		if len(lines) >= 2 {
			fields := strings.Fields(lines[len(lines)-1])
			if len(fields) >= 5 {
				b.WriteString("Диск /: " + strings.Join(fields[1:5], " ") + "\n")
			}
		}
	}
	if services := strings.TrimSpace(os.Getenv("DASKGRAM_OPS_SERVICES")); services != "" {
		b.WriteString("Сервисы: " + serviceStates(strings.Fields(services)) + "\n")
	}
	return strings.TrimSpace(b.String())
}

func serviceStates(names []string) string {
	parts := make([]string, 0, len(names))
	for _, name := range names {
		out, _ := runCmd("systemctl", "is-active", name)
		state := strings.TrimSpace(out)
		if state == "" {
			state = "unknown"
		}
		parts = append(parts, name+" "+state)
	}
	return strings.Join(parts, " · ")
}

func runCmd(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).Output()
	return string(out), err
}

func memory() (total, available int64, ok bool) {
	raw, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, 0, false
	}
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		value, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			continue
		}
		switch fields[0] {
		case "MemTotal:":
			total = value * 1024
		case "MemAvailable:":
			available = value * 1024
		}
	}
	return total, available, total > 0
}

func gb(bytes int64) string { return fmt.Sprintf("%.1f GB", float64(bytes)/(1<<30)) }

func fatal(message string) {
	fmt.Fprintln(os.Stderr, "daskgram-ops:", message)
	os.Exit(1)
}
