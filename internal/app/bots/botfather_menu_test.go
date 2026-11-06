package bots

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"telesrv/internal/domain"
)

// botFatherInlineButtonTexts returns the labels of an inline keyboard in reading
// order, and fails the test when the markup is not inline at all.
func botFatherInlineButtonTexts(t *testing.T, msg domain.Message) []string {
	t.Helper()
	if msg.ReplyMarkup == nil || msg.ReplyMarkup.Kind() != domain.MessageReplyMarkupInline {
		t.Fatalf("reply %q does not carry an inline keyboard: %+v", msg.Body, msg.ReplyMarkup)
	}
	var out []string
	for _, row := range msg.ReplyMarkup.Inline {
		for _, button := range row {
			if button.Type != domain.MarkupButtonCallback {
				t.Fatalf("button %q type = %q, want callback", button.Text, button.Type)
			}
			out = append(out, button.Text)
		}
	}
	return out
}

func clickBotFather(t *testing.T, svc *Service, owner domain.User, data string) {
	t.Helper()
	answer, handled, err := svc.onBotFatherCallback(context.Background(), domain.BotCallbackQuery{
		BotUserID: domain.BotFatherUserID,
		UserID:    owner.ID,
		Data:      []byte(data),
	})
	if err != nil || !handled {
		t.Fatalf("callback %q: handled=%v err=%v", data, handled, err)
	}
	if answer.Message != "" {
		t.Fatalf("callback %q answered with an alert: %+v", data, answer)
	}
}

// /start opens a menu instead of a wall of command text: three buttons, and the
// full command list one click away.
func TestBotFatherStartOpensButtonMenu(t *testing.T) {
	svc, users, _, messages := newTestService(t)
	owner := newOwner(t, users, "+2000")

	reply := latestBotFatherMessage(t, svc, messages, owner, "/start")
	labels := botFatherInlineButtonTexts(t, reply)
	want := []string{"My bots", "New bot", "All commands"}
	if strings.Join(labels, "|") != strings.Join(want, "|") {
		t.Fatalf("menu buttons = %v, want %v", labels, want)
	}
}

// /mybots renders one button per bot, and each button opens that bot's card.
func TestBotFatherMyBotsRendersInlineList(t *testing.T) {
	svc, users, _, messages := newTestService(t)
	owner := newOwner(t, users, "+2001")
	ctx := context.Background()

	created, _, err := svc.CreateBot(ctx, owner.ID, "Shop Bot", "shop_bot")
	if err != nil {
		t.Fatalf("create bot: %v", err)
	}

	reply := latestBotFatherMessage(t, svc, messages, owner, "/mybots")
	labels := botFatherInlineButtonTexts(t, reply)
	if len(labels) == 0 || labels[0] != "@shop_bot" {
		t.Fatalf("bot list buttons = %v, want @shop_bot", labels)
	}

	clickBotFather(t, svc, owner, "bf:bot:"+botIDString(created.ID))
	card := lastBotFatherReply(t, messages, owner.ID)
	if !strings.Contains(card.Body, "@shop_bot") {
		t.Fatalf("card body = %q, want the bot username", card.Body)
	}
	cardLabels := botFatherInlineButtonTexts(t, card)
	for _, want := range []string{"Token", "Name", "Commands", "Delete bot", "Back"} {
		if !containsLabel(cardLabels, want) {
			t.Fatalf("card buttons = %v, want %q", cardLabels, want)
		}
	}
}

// A card button runs the same command a typed /command would: here /token.
func TestBotFatherCardButtonRunsCommand(t *testing.T) {
	svc, users, _, messages := newTestService(t)
	owner := newOwner(t, users, "+2002")
	ctx := context.Background()

	created, _, err := svc.CreateBot(ctx, owner.ID, "Token Bot", "token_bot")
	if err != nil {
		t.Fatalf("create bot: %v", err)
	}

	clickBotFather(t, svc, owner, "bf:do:"+botFatherCmdToken+":"+botIDString(created.ID))
	reply := lastBotFatherReply(t, messages, owner.ID)
	if !strings.Contains(reply.Body, "HTTP API") {
		t.Fatalf("token reply = %q, want the token message", reply.Body)
	}
}

// The callback names a bot id, and the handler resolves it against the caller's
// own bots: another owner's bot is refused even though the id is valid.
func TestBotFatherCallbackRefusesForeignBot(t *testing.T) {
	svc, users, _, messages := newTestService(t)
	owner := newOwner(t, users, "+2003")
	other := newOwner(t, users, "+2004")
	ctx := context.Background()

	foreign, _, err := svc.CreateBot(ctx, other.ID, "Other Bot", "other_bot")
	if err != nil {
		t.Fatalf("create bot: %v", err)
	}

	clickBotFather(t, svc, owner, "bf:bot:"+botIDString(foreign.ID))
	reply := lastBotFatherReply(t, messages, owner.ID)
	if !strings.Contains(reply.Body, "not yours") {
		t.Fatalf("foreign bot reply = %q, want a refusal", reply.Body)
	}

	clickBotFather(t, svc, owner, "bf:do:"+botFatherCmdDeleteBot+":"+botIDString(foreign.ID))
	reply = lastBotFatherReply(t, messages, owner.ID)
	if !strings.Contains(reply.Body, "not yours") {
		t.Fatalf("foreign delete reply = %q, want a refusal", reply.Body)
	}
}

// Garbage callback data is never an action: unparseable data gets an alert, and
// a command outside the allow-list falls back to the bot list.
func TestBotFatherCallbackRejectsUnknownData(t *testing.T) {
	svc, users, _, messages := newTestService(t)
	owner := newOwner(t, users, "+2005")

	answer, handled, err := svc.onBotFatherCallback(context.Background(), domain.BotCallbackQuery{
		BotUserID: domain.BotFatherUserID,
		UserID:    owner.ID,
		Data:      []byte("garbage"),
	})
	if !handled || err != nil {
		t.Fatalf("handled=%v err=%v", handled, err)
	}
	if answer.Message == "" || !answer.Alert {
		t.Fatalf("unparseable answer = %+v, want an alert", answer)
	}

	clickBotFather(t, svc, owner, "bf:do:dropdatabase:1")
	reply := lastBotFatherReply(t, messages, owner.ID)
	if !strings.Contains(reply.Body, "not yours") {
		t.Fatalf("unknown command reply = %q, want a refusal", reply.Body)
	}
}

func containsLabel(labels []string, want string) bool {
	for _, label := range labels {
		if label == want {
			return true
		}
	}
	return false
}

func botIDString(id int64) string {
	return strconv.FormatInt(id, 10)
}
