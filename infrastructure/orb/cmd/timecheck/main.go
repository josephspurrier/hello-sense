// timecheck does one real time-sync round trip against a running orb and
// verifies the clock it hands back.
//
// This exists because handing the Sense a wrong clock is the single action in
// the edge that can silently corrupt an entire night's data: the device stamps
// its own samples, and a bad offset makes every one of them land in the wrong
// place. The unit test in internal/edge only checks that the reply is signed,
// so it would not have caught the Unix-vs-NTP epoch bug that put the device in
// 1956. This checks the decoded value.
//
// It never prints the device key.
package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"time"

	"google.golang.org/protobuf/proto"

	pbntp "github.com/josephspurrier/hello-orb/orb/internal/pb/ntp"
	"github.com/josephspurrier/hello-orb/orb/internal/sense"
	"github.com/josephspurrier/hello-orb/orb/internal/store"
)

// ntpEpochOffset is seconds between 1900-01-01 and 1970-01-01. The device
// speaks NTP; Unix time here is what caused the 1956 timestamps.
const ntpEpochOffset = 2208988800

func main() {
	var (
		dsn    = flag.String("dsn", "postgres://hello:hello@localhost:5432/orb", "Postgres DSN")
		target = flag.String("target", "http://127.0.0.1:8081/", "orb edge URL")
		host   = flag.String("host", "time.hello.is", "Host header to route on")
		dev    = flag.String("device", "49F277D951568DF3", "device id")
	)
	flag.Parse()

	ctx := context.Background()
	st, err := store.Open(ctx, *dsn)
	if err != nil {
		log.Fatalf("open store: %v", err)
	}
	defer st.Close()

	d, err := st.SenseByID(ctx, *dev)
	if err != nil {
		log.Fatalf("lookup device: %v", err)
	}

	// Build the request the way the firmware does: payload, then the IV and
	// signature as a trailer.
	origin := int64(time.Now().UTC().Unix()+ntpEpochOffset) << 32
	payload, err := proto.Marshal(&pbntp.NTPDataPacket{OriginTs: proto.Int64(origin)})
	if err != nil {
		log.Fatalf("marshal: %v", err)
	}
	signed, err := sense.Sign(d.AESKey, payload)
	if err != nil {
		log.Fatalf("sign: %v", err)
	}
	body := append(append([]byte{}, payload...), signed[:sense.TrailerLength]...)

	req, err := http.NewRequest(http.MethodPost, *target, bytes.NewReader(body))
	if err != nil {
		log.Fatalf("request: %v", err)
	}
	req.Host = *host
	req.Header.Set("X-Hello-Sense-Id", *dev)
	req.Header.Set("Content-Type", "application/x-protobuf")

	sent := time.Now()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	buf := new(bytes.Buffer)
	buf.ReadFrom(resp.Body)

	fmt.Printf("status        %d\n", resp.StatusCode)
	if resp.StatusCode != http.StatusOK {
		log.Fatalf("want 200, body=%q", buf.String())
	}

	// The reply is IV|sig|payload, the opposite framing from the request.
	out := buf.Bytes()
	if len(out) <= sense.TrailerLength {
		log.Fatalf("short reply: %d bytes", len(out))
	}
	iv, sig, pl := out[:sense.IVLength], out[sense.IVLength:sense.TrailerLength], out[sense.TrailerLength:]
	if err := sense.Verify(d.AESKey, pl, iv, sig); err != nil {
		log.Fatalf("the device would REJECT this reply: %v", err)
	}
	fmt.Println("signature     valid (the device would accept it)")

	var pkt pbntp.NTPDataPacket
	if err := proto.Unmarshal(pl, &pkt); err != nil {
		log.Fatalf("unmarshal reply: %v", err)
	}

	if pkt.GetOriginTs() != origin {
		log.Fatalf("origin_ts echoed back as %d, want %d", pkt.GetOriginTs(), origin)
	}
	fmt.Println("origin_ts     echoed correctly")

	// Decode the fixed-point NTP timestamp back to a wall clock.
	fmt.Printf("raw transmit  %d\n", pkt.GetTransmitTs())
	secs := int64(uint64(pkt.GetTransmitTs()) >> 32)
	got := time.Unix(secs-ntpEpochOffset, 0).UTC()
	fmt.Printf("transmit_ts   %s UTC\n", got.Format("2006-01-02 15:04:05"))

	skew := got.Sub(sent.UTC())
	if skew < 0 {
		skew = -skew
	}
	fmt.Printf("skew vs host  %s\n", skew.Truncate(time.Millisecond))
	if got.Year() < 2020 {
		log.Fatalf("FAIL: clock is in %d, the epoch is wrong", got.Year())
	}
	if skew > 5*time.Second {
		log.Fatalf("FAIL: skew of %s is too large", skew)
	}
	fmt.Println("\nPASS: orb hands the device a correct, correctly-signed clock.")
}
