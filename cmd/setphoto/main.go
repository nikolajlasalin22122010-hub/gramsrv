// Command setphoto uploads a local image and sets it as the profile photo of a
// user, bot or channel. Used for bootstrapping avatars without a client upload.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"

	filesapp "telesrv/internal/app/files"
	"telesrv/internal/domain"
	"telesrv/internal/store/postgres"
)

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func main() {
	dsn := flag.String("dsn", envOr("TELESRV_POSTGRES_DSN", ""), "postgres dsn")
	blobDir := flag.String("blob-dir", envOr("TELESRV_BLOB_DIR", "data/blobs"), "blob root directory")
	dc := flag.Int("dc", 2, "datacenter id")
	ownerType := flag.String("owner-type", "user", "peer type: user or channel")
	ownerID := flag.Int64("owner-id", 0, "owner peer id")
	file := flag.String("file", "", "path to the image")
	flag.Parse()

	if *dsn == "" || *ownerID == 0 || *file == "" {
		fmt.Fprintln(os.Stderr, "usage: setphoto -dsn <dsn> -owner-id <id> -file <image> [-owner-type user|channel]")
		os.Exit(2)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	data, err := os.ReadFile(*file)
	if err != nil {
		fmt.Fprintf(os.Stderr, "read image: %v\n", err)
		os.Exit(1)
	}

	pool, err := pgxpool.New(ctx, *dsn)
	if err != nil {
		fmt.Fprintf(os.Stderr, "connect: %v\n", err)
		os.Exit(1)
	}
	defer pool.Close()

	blobs, err := filesapp.NewLocalFS(*blobDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "blob fs: %v\n", err)
		os.Exit(1)
	}
	media := postgres.NewMediaStore(pool)
	svc := filesapp.NewService(media, filesapp.BlobBackend(blobs), *dc, filesapp.WithLogger(zap.NewNop()))

	photo, err := svc.CreateAvatarFromBytes(ctx, data)
	if err != nil {
		fmt.Fprintf(os.Stderr, "create avatar: %v\n", err)
		os.Exit(1)
	}

	peerType := domain.PeerTypeUser
	if *ownerType == "channel" {
		peerType = domain.PeerTypeChannel
	}
	if err := media.AddProfilePhotoKind(ctx, peerType, *ownerID, domain.ProfilePhotoKindProfile, photo.ID, int(time.Now().Unix())); err != nil {
		fmt.Fprintf(os.Stderr, "bind photo: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("ok photo_id=%d sizes=%d owner=%s:%d\n", photo.ID, len(photo.Sizes), peerType, *ownerID)
}
