// Command pushsend sends one Apple push notification by hand.
//
// It exists for two jobs. The first is proving the credential chain without a
// phone: send to a well-formed but fake device token and read the reason Apple
// gives back. BadDeviceToken means the key, key id, team id, topic and TLS are
// all correct and only the token is wrong, which is exactly what you want to
// see before blaming any of the others. InvalidProviderToken means the
// credentials are wrong and the token never got looked at.
//
// The second is sending a real notification once a device has registered.
//
//	go run ./cmd/pushsend -key ../push-key/AuthKey_XXXXXXXXXX.p8 \
//	  -key-id XXXXXXXXXX -team FCYLFD2LML -topic com.example.app \
//	  -token <hex> -title "Hello" -body "It works"
//
// With no -token it reads every registered token from the database instead.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/josephspurrier/hello-orb/orb/internal/push"
	"github.com/josephspurrier/hello-orb/orb/internal/store"
)

func main() {
	var (
		keyPath = flag.String("key", "", "path to the .p8 signing key")
		keyID   = flag.String("key-id", "", "the ten characters from the key filename")
		team    = flag.String("team", "", "Apple developer team id")
		topic   = flag.String("topic", "", "the app's bundle id")
		token   = flag.String("token", "", "device token; omit to send to every registered token")
		title   = flag.String("title", "Sense", "notification title")
		body    = flag.String("body", "Test from orb.", "notification body")
		prod    = flag.Bool("production", false, "use the production APNS host instead of sandbox")
		dsn     = flag.String("dsn", os.Getenv("ORB_DSN"), "postgres dsn, used only when -token is omitted")
	)
	flag.Parse()

	host := push.HostSandbox
	if *prod {
		host = push.HostProduction
	}

	client, err := push.New(push.Config{
		KeyPath: *keyPath, KeyID: *keyID, TeamID: *team, Topic: *topic, Host: host,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	tokens := []string{*token}
	if *token == "" {
		tokens, err = registeredTokens(ctx, *dsn)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		if len(tokens) == 0 {
			fmt.Fprintln(os.Stderr, "no registered device tokens; open the app on the phone first")
			os.Exit(1)
		}
	}

	failed := false
	for _, t := range tokens {
		err := client.Send(ctx, t, push.Alert{Title: *title, Body: *body})
		switch {
		case err == nil:
			fmt.Printf("sent   %s\n", short(t))
		case errors.Is(err, push.ErrUnregistered):
			// Worth calling out separately: this is the one failure that means
			// the credentials are fine.
			fmt.Printf("token rejected, credentials accepted   %s: %v\n", short(t), err)
			failed = true
		default:
			fmt.Printf("failed %s: %v\n", short(t), err)
			failed = true
		}
	}
	if failed {
		os.Exit(1)
	}
}

func registeredTokens(ctx context.Context, dsn string) ([]string, error) {
	if dsn == "" {
		return nil, errors.New("need -dsn or ORB_DSN to look up registered tokens")
	}
	s, err := store.Open(ctx, dsn)
	if err != nil {
		return nil, err
	}
	rows, err := s.AllPushTokens(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Token)
	}
	return out, nil
}

// short keeps a device token out of the terminal in full. It is not a secret in
// the way the signing key is, but it identifies a phone and there is no reason
// to paste it around.
func short(t string) string {
	if len(t) <= 12 {
		return t
	}
	return t[:8] + "..." + t[len(t)-4:]
}
