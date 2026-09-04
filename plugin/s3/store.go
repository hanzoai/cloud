// Copyright (c) 2026 Hanzo AI Inc.

package main

// This plugin does not TALK to an object store, it IS one.
//
// github.com/hanzoai/s3 is linked here and started in this process, so the
// bytes a caller PUTs live in a volume this binary owns. The arrangement it
// replaces pointed S3_ADMIN_ENDPOINT at a separate server and kept only a
// client here, which is why deleting that server left /v1/s3/health answering
// 200 with nothing behind it: the route surface and the store were two
// lifetimes, and only one of them was ours to keep alive.
//
// The fork's command table is its library entry point. `mini` composes master,
// volume, filer and the S3 API over one directory, and its Run blocks for the
// life of the process, so it runs on its own goroutine and cloud.Listen keeps
// the main one. Nothing is shelled out and nothing is forked.
//
// WHY THE S3 PROTOCOL STILL APPEARS BETWEEN THE TWO HALVES. The fork publishes
// a native ZAP object service (s3/svc/object) whose surface is GetObject and
// PutObject. apps/s3 also lists, deletes, copies, presigns and drives multipart
// uploads, and s3admin builds ONE client for all of it — so binding the two
// halves over ZAP today would mean the four verbs that service has and 503 for
// the rest. Until it covers them, the in-process interface is the S3 API on
// loopback. That is a listener this process opened and this process answers:
// one lifetime, one thing to start, nothing to point at a second server.

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/hanzoai/s3/s3/command"
	util_http "github.com/hanzoai/s3/s3/util/http"
)

// startStore brings the object store up inside this process, at the address
// S3_ADMIN_ENDPOINT names, and returns once it accepts connections.
//
// THE STORE BINDS THE ADDRESS ITS CLIENTS DIAL. One value, read by everyone:
// apps/sites, apps/space, apps/projects, apps/platform, apps/meet and the host's
// own webui/release each build an s3admin client from S3_ADMIN_ENDPOINT, and
// every one of them is a DIFFERENT process. An address this process chose for
// itself would be an address none of them could learn — so the endpoint is
// configuration, and what this function does is answer at it rather than pick it.
//
// Only the S3 API is fixed. Master, volume and filer get whatever ports are
// free, because nothing outside this process dials them and a second cloud on
// the same box would otherwise collide on all four.
func startStore(dataDir string) error {
	ak, sk := os.Getenv("S3_ADMIN_ACCESS_KEY"), os.Getenv("S3_ADMIN_SECRET_KEY")
	if ak == "" || sk == "" {
		return fmt.Errorf("S3_ADMIN_ACCESS_KEY/S3_ADMIN_SECRET_KEY are unset, so the store has no admin identity to create; both come from KMS (hanzo-s3)")
	}
	// The fork mints its admin identity from the AWS names, which is how the
	// credential reaches it without a config file on disk or a secret in an argv
	// any process on the box can read out of /proc.
	os.Setenv("AWS_ACCESS_KEY_ID", ak)
	os.Setenv("AWS_SECRET_ACCESS_KEY", sk)

	dir := filepath.Join(dataDir, "s3")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("s3 store dir: %w", err)
	}

	addr := os.Getenv("S3_ADMIN_ENDPOINT")
	if addr == "" {
		addr = defaultAddr
		os.Setenv("S3_ADMIN_ENDPOINT", addr)
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("S3_ADMIN_ENDPOINT %q is not host:port: %w", addr, err)
	}
	// Fail here rather than let the store quietly pick a neighbouring port: the
	// clients dial the number in the environment, so a store that moved is a
	// store nobody can find, which reads exactly like the empty backend this
	// whole change exists to end.
	if l, err := net.Listen("tcp", addr); err != nil {
		return fmt.Errorf("s3 store cannot bind %s (S3_ADMIN_ENDPOINT): %w", addr, err)
	} else {
		l.Close()
	}
	// Internal to the store — nothing outside this process dials them, so they
	// are taken from whatever is free rather than pinned.
	inner, err := freePorts(4)
	if err != nil {
		return err
	}

	mini := lookup("mini")
	if mini == nil {
		return fmt.Errorf("github.com/hanzoai/s3 has no runnable mini command")
	}
	// Who the store will take orders from — see iam.go. Written before the store
	// starts because the file is a startup flag, and empty on a deployment that
	// names no issuer, which then runs on the admin credential alone.
	iam, err := configure(dataDir)
	if err != nil {
		return err
	}
	util_http.InitGlobalHttpClient()
	args := []string{
		"-dir=" + dir,
		"-ip=" + host,
		"-ip.bind=" + host,
		"-s3.port=" + port,
		fmt.Sprintf("-master.port=%d", inner[0]),
		fmt.Sprintf("-volume.port=%d", inner[1]),
		fmt.Sprintf("-filer.port=%d", inner[2]),
		fmt.Sprintf("-admin.port=%d", inner[3]),
		// Off because this process serves the S3 API and nothing else: the
		// Iceberg catalog, WebDAV and the admin UI are three more listeners with
		// no caller here, and the admin UI's assets live in the fork's own main.
		"-s3.port.iceberg=0",
		"-webdav=false",
		"-admin.ui=false",
	}
	if iam != "" {
		args = append(args, "-s3.iam.config="+iam)
	}
	if err := mini.Flag.Parse(args); err != nil {
		return fmt.Errorf("s3 store flags: %w", err)
	}
	go mini.Run(mini, mini.Flag.Args())

	// Ready is the port accepting, never a timer: a cold store builds master,
	// volume and filer before the S3 API binds, and a deadline shorter than that
	// reports an outage it caused.
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		if c, err := net.DialTimeout("tcp", addr, time.Second); err == nil {
			c.Close()
			return nil
		}
		time.Sleep(250 * time.Millisecond)
	}
	return fmt.Errorf("s3 store did not accept on %s within 90s", addr)
}

// defaultAddr is where the store answers when nothing says otherwise. It is
// loopback because the store and everything that reads it are processes of one
// pod, and 8333 because that is the port hanzoai/s3 has always served S3 on.
const defaultAddr = "127.0.0.1:8333"

// lookup finds one command in the fork's exported table.
func lookup(name string) *command.Command {
	for _, c := range command.Commands {
		if c.Name() == name && c.Run != nil {
			return c
		}
	}
	return nil
}

// freePorts reserves n distinct loopback ports by binding them, then hands back
// the numbers. The listeners are held until every port is chosen so the kernel
// cannot hand the same one out twice.
func freePorts(n int) ([]int, error) {
	held := make([]net.Listener, 0, n)
	defer func() {
		for _, l := range held {
			l.Close()
		}
	}()
	out := make([]int, 0, n)
	for range n {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return nil, fmt.Errorf("reserve s3 store port: %w", err)
		}
		held = append(held, l)
		out = append(out, l.Addr().(*net.TCPAddr).Port)
	}
	return out, nil
}
