// Command odm downloads a URL with parallel range requests.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/marXus-3D/odm/internal/engine"
)

func main() {
	var (
		conns = flag.Int("n", engine.DefaultMaxConns, "parallel connections")
		dir   = flag.String("d", "", "output directory (default: user Downloads)")
		out   = flag.String("o", "", "output filename")
		quiet = flag.Bool("q", false, "suppress the progress line")
		ref   = flag.String("referer", "", "Referer header")
		ua    = flag.String("ua", "", "User-Agent header")
		cook  = flag.String("cookie", "", "Cookie header")
		limit = flag.Int("limit", 0, "speed limit in KiB/s (0 = unlimited)")
	)
	flag.Usage = usage
	// Subcommands are dispatched before flag parsing so that "odm add -n 4 url"
	// hands its flags to the subcommand rather than to the top-level set.
	if len(os.Args) > 1 && runCommand(os.Args[1:]) {
		return
	}
	flag.Parse()
	if flag.NArg() != 1 {
		flag.Usage()
		os.Exit(2)
	}

	headers := map[string]string{}
	if *ref != "" {
		headers["Referer"] = *ref
	}
	if *ua != "" {
		headers["User-Agent"] = *ua
	}
	if *cook != "" {
		headers["Cookie"] = *cook
	}

	req := engine.Request{
		URL:      flag.Arg(0),
		Dir:      *dir,
		Filename: *out,
		Headers:  headers,
		MaxConns: *conns,
	}

	opts := engine.Options{}
	if *limit > 0 {
		opts.Limiter = engine.NewLimiter(float64(*limit) * 1024)
	}
	d := engine.New("cli", req, opts)
	if !*quiet {
		d.OnUpdate = func(s engine.Stats) { renderProgress(d, s) }
	}

	// First Ctrl-C pauses cleanly so the sidecar is written; a second one
	// aborts outright.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		fmt.Fprintln(os.Stderr, "\ninterrupted, saving resume state...")
		d.Pause()
	}()

	start := time.Now()
	err := d.Run(context.Background())
	if !*quiet {
		fmt.Fprintln(os.Stderr)
	}

	switch {
	case errors.Is(err, engine.ErrPaused):
		fmt.Fprintf(os.Stderr, "paused at %s -- rerun the same command to resume\n",
			humanBytes(d.Downloaded()))
		os.Exit(1)
	case err != nil:
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	el := time.Since(start)
	fmt.Printf("%s  %s in %s (%s/s)\n", d.Path, humanBytes(d.Downloaded()),
		el.Round(time.Millisecond),
		humanBytes(int64(float64(d.Downloaded())/el.Seconds())))
}

func usage() {
	fmt.Fprint(os.Stderr, `usage:
  odm [flags] <url>           download now, in this process
  odm add [flags] <url>...    queue in the daemon (starts it if needed)
                             -q <queue> picks a queue, -now skips the dialog
  odm ls                      list what the daemon knows about
  odm pause|resume <id>...    control a queued download
  odm rm [-f] <id>...         forget one; -f also deletes the file
  odm open|show <id>          open the file, or reveal it in Explorer
  odm pause-all               pause everything that is running
  odm resume-all              resume everything unfinished
  odm stop-all                pause everything and clear the queue
  odm startup [on|off]        show or set whether ODM runs at login
  odm on-finish [<action>]    what to do when everything finishes:
                             none exit sleep hibernate shutdown restart
                             (or "cancel" to call off a pending one)
  odm queues                 list the download queues
  odm queue add [-n N] <name>  create a queue
  odm queue set [-n N] [-name NEW] <queue>
  odm queue rm <queue>       delete one; its downloads move to the default
  odm move <id>... <queue>   send downloads to another queue
  odm limit [<KiB/s>|off]     show or set the global speed limit
  odm ui                      print the web UI url
  odm daemon [stop]           daemon status, or stop it gracefully

flags for the direct form:
`)
	flag.PrintDefaults()
}

var lastLen int

func renderProgress(d *engine.Download, s engine.Stats) {
	var bar string
	if s.Total > 0 {
		pct := float64(s.Downloaded) / float64(s.Total)
		const width = 28
		filled := int(pct * width)
		bar = fmt.Sprintf("[%s%s] %5.1f%%",
			strings.Repeat("=", filled), strings.Repeat(" ", width-filled), pct*100)
	} else {
		bar = "[  streaming  ]"
	}

	eta := "--:--"
	if s.ETA > 0 {
		eta = fmt.Sprintf("%02d:%02d", int(s.ETA.Minutes()), int(s.ETA.Seconds())%60)
	}
	line := fmt.Sprintf("\r%s %9s/%-9s %9s/s  %2d conn  %2d seg  ETA %s",
		bar, humanBytes(s.Downloaded), humanBytes(s.Total),
		humanBytes(int64(s.SpeedBPS)), s.Conns, len(s.Segments), eta)

	// Pad over whatever the previous, possibly longer, line left behind.
	if pad := lastLen - len(line); pad > 0 {
		line += strings.Repeat(" ", pad)
	}
	lastLen = len(line)
	fmt.Fprint(os.Stderr, line)
}

func humanBytes(n int64) string {
	if n < 0 {
		return "?"
	}
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit && exp < 4; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTP"[exp])
}
