package main

import (
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/marcus/dm/internal/client"
	"github.com/marcus/dm/internal/store"
)

// runCommand handles the daemon-backed subcommands. It returns false when
// args[0] is not a subcommand, so the caller can fall back to a direct
// standalone download.
func runCommand(args []string) bool {
	switch args[0] {
	case "add", "ls", "list", "pause", "resume", "rm", "remove",
		"open", "show", "ui", "limit", "daemon",
		"pause-all", "resume-all", "stop-all", "startup":
	default:
		return false
	}

	cmd, rest := args[0], args[1:]
	c, err := client.Connect("")
	if err != nil {
		fail("%v", err)
	}

	switch cmd {
	case "add":
		cmdAdd(c, rest)
	case "ls", "list":
		cmdList(c)
	case "pause":
		forEachID(rest, "pause", "paused", c.Pause)
	case "resume":
		forEachID(rest, "resume", "resumed", c.Resume)
	case "rm", "remove":
		cmdRemove(c, rest)
	case "open":
		forEachID(rest, "open", "opened", c.Open)
	case "show":
		forEachID(rest, "show", "revealed", c.Reveal)
	case "ui":
		fmt.Println(c.Base + "/")
	case "limit":
		cmdLimit(c, rest)
	case "daemon":
		cmdDaemon(c, rest)
	case "pause-all":
		must(c.PauseAll(), "everything paused")
	case "resume-all":
		must(c.ResumeAll(), "everything resumed")
	case "stop-all":
		must(c.StopAll(), "everything stopped")
	case "startup":
		cmdStartup(c, rest)
	}
	return true
}

func cmdAdd(c *client.Client, args []string) {
	fs := flag.NewFlagSet("add", flag.ExitOnError)
	conns := fs.Int("n", 0, "parallel connections (0 = use the daemon default)")
	dir := fs.String("d", "", "output directory")
	out := fs.String("o", "", "output filename")
	ref := fs.String("referer", "", "Referer header")
	cook := fs.String("cookie", "", "Cookie header")
	ua := fs.String("ua", "", "User-Agent header")
	fs.Parse(args)

	if fs.NArg() == 0 {
		fail("usage: dm add [flags] <url>...")
	}
	for _, u := range fs.Args() {
		rec, err := c.Add(client.AddRequest{
			URL: u, Dir: *dir, Filename: *out, MaxConns: *conns,
			Referer: *ref, Cookie: *cook, UA: *ua,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "dm: add %s: %v\n", u, err)
			continue
		}
		fmt.Printf("%s  queued  %s\n", rec.ID, rec.URL)
	}
}

func cmdList(c *client.Client) {
	st, err := c.State()
	if err != nil {
		fail("%v", err)
	}
	if len(st.Downloads) == 0 {
		fmt.Println("no downloads")
		return
	}
	sort.Slice(st.Downloads, func(i, j int) bool {
		return st.Downloads[i].Created.After(st.Downloads[j].Created)
	})

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tSTATE\tPROGRESS\tSIZE\tNAME")
	for _, r := range st.Downloads {
		pct := "-"
		if r.Size > 0 {
			pct = fmt.Sprintf("%.0f%%", float64(r.Downloaded)/float64(r.Size)*100)
		}
		name := r.Filename
		if name == "" {
			name = r.URL
		}
		if len(name) > 52 {
			name = name[:49] + "..."
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
			r.ID, r.State, pct, humanBytes(r.Size), name)
	}
	w.Flush()

	if st.Config.LimitKBps > 0 {
		fmt.Printf("\nspeed limit: %d KiB/s\n", st.Config.LimitKBps)
	}
}

func cmdRemove(c *client.Client, args []string) {
	fs := flag.NewFlagSet("rm", flag.ExitOnError)
	del := fs.Bool("f", false, "also delete the file from disk")
	fs.Parse(args)
	if fs.NArg() == 0 {
		fail("usage: dm rm [-f] <id>...")
	}
	for _, id := range fs.Args() {
		if err := c.Remove(id, *del); err != nil {
			fmt.Fprintf(os.Stderr, "dm: rm %s: %v\n", id, err)
			continue
		}
		fmt.Printf("%s removed\n", id)
	}
}

func cmdLimit(c *client.Client, args []string) {
	if len(args) == 0 {
		st, err := c.State()
		if err != nil {
			fail("%v", err)
		}
		if st.Config.LimitKBps == 0 {
			fmt.Println("no speed limit")
		} else {
			fmt.Printf("%d KiB/s\n", st.Config.LimitKBps)
		}
		return
	}

	var kbps int
	arg := strings.TrimSpace(args[0])
	if arg == "off" || arg == "none" {
		kbps = 0
	} else if _, err := fmt.Sscanf(arg, "%d", &kbps); err != nil || kbps < 0 {
		fail("usage: dm limit [<KiB/s>|off]")
	}
	cfg, err := c.SetConfig(store.Config{LimitKBps: kbps})
	if err != nil {
		fail("%v", err)
	}
	if cfg.LimitKBps == 0 {
		fmt.Println("speed limit removed")
	} else {
		fmt.Printf("speed limit set to %d KiB/s\n", cfg.LimitKBps)
	}
}

// must reports a failed whole-list operation, or prints what happened.
func must(err error, done string) {
	if err != nil {
		fail("%v", err)
	}
	fmt.Println(done)
}

// cmdStartup shows or changes whether DM runs at login.
func cmdStartup(c *client.Client, args []string) {
	if len(args) == 0 {
		st, err := c.State()
		if err != nil {
			fail("%v", err)
		}
		if !st.StartWithWindowsSupported {
			fmt.Println("run at login is not supported on this platform")
			return
		}
		if st.StartWithWindows {
			fmt.Println("DM runs at login")
		} else {
			fmt.Println("DM does not run at login")
		}
		return
	}
	on := args[0] == "on" || args[0] == "enable" || args[0] == "true"
	if !on && args[0] != "off" && args[0] != "disable" && args[0] != "false" {
		fail("usage: dm startup [on|off]")
	}
	if _, err := c.SetFlags(client.Flags{StartWithWindows: &on}); err != nil {
		fail("%v", err)
	}
	if on {
		fmt.Println("DM will run at login")
	} else {
		fmt.Println("DM will no longer run at login")
	}
}

func cmdDaemon(c *client.Client, args []string) {
	if len(args) > 0 && (args[0] == "stop" || args[0] == "quit") {
		if err := c.Shutdown(); err != nil {
			fail("%v", err)
		}
		fmt.Println("daemon stopping; running downloads were paused and can be resumed")
		return
	}
	fmt.Printf("daemon is up at %s\n", c.Base)
}

// forEachID applies fn to every id, reporting failures per id rather than
// aborting the batch. done is the past tense used in the success line.
func forEachID(ids []string, verb, done string, fn func(string) error) {
	if len(ids) == 0 {
		fail("usage: dm %s <id>...", verb)
	}
	for _, id := range ids {
		if err := fn(id); err != nil {
			fmt.Fprintf(os.Stderr, "dm: %s %s: %v\n", verb, id, err)
			continue
		}
		fmt.Printf("%s %s\n", id, done)
	}
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "dm: "+format+"\n", args...)
	os.Exit(1)
}
