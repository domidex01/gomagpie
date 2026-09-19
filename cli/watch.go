package cli

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"time"

	"github.com/domidex01/magpie/scrape"

	"github.com/spf13/cobra"
)

// watchEveryFloor is the minimum loop interval: watch is a cron-shaped
// check, not a load tool; faster polling belongs to a crawler.
const watchEveryFloor = 30 * time.Second

func newWatchCmd() *cobra.Command {
	var every, webhook string
	var once bool
	var render, lang string
	cmd := &cobra.Command{
		Use:   "watch <url>",
		Short: "Watch a URL for changes: snapshot → word-diff → webhook",
		Long: `Store a markdown snapshot of the URL, then report a word-diff whenever
the content changes. --once runs a single check (the cron/systemd-timer
mode); otherwise the loop repeats every --every until Ctrl-C (exit 0).
On change (and only on change) the word-diff prints and --webhook
receives one POST {url, changed, old_hash, new_hash, diff}. Silence on
no-change, like magpie diff. Zero LLM.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runWatch(cmd.Context(), args[0], watchOptions{
				Every: every, Webhook: webhook, Once: once, Render: render, Lang: lang,
			})
		},
	}
	cmd.Flags().StringVar(&every, "every", "", "check interval, e.g. 5m, 1h (minimum 30s; required)")
	cmd.Flags().BoolVar(&once, "once", false, "run a single check and exit (cron/systemd-timer mode)")
	cmd.Flags().StringVar(&webhook, "webhook", "", "URL to POST {url, changed, old_hash, new_hash, diff} on change")
	cmd.Flags().StringVar(&render, "render", "", "auto|static|browser")
	cmd.Flags().StringVar(&lang, "lang", "", "Accept-Language header value, e.g. fr-CA,fr;q=0.9")
	return cmd
}

type watchOptions struct {
	Every   string
	Webhook string
	Once    bool
	Render  string
	Lang    string
}

func runWatch(ctx context.Context, rawURL string, o watchOptions) error {
	if o.Every == "" {
		return fail(2, "watch: --every is required (e.g. --every 5m)")
	}
	interval, err := time.ParseDuration(o.Every)
	if err != nil {
		return fail(2, "watch: --every %q is not a duration (e.g. 5m, 1h)", o.Every)
	}
	if interval < watchEveryFloor {
		return fail(2, "watch: --every %s is below the %s floor", o.Every, watchEveryFloor)
	}
	if err := scrape.ValidateOptions(scrape.Options{Render: o.Render, Lang: o.Lang}); err != nil {
		return err
	}
	cfg, err := resolveConfig()
	if err != nil {
		return err
	}
	db, err := openCmdDB(cfg)
	if err != nil {
		return err
	}
	defer closeDB(db)
	deps := scrapeDeps(db, cfg)
	opts := scrape.Options{Render: o.Render, Lang: o.Lang, Webhook: o.Webhook}

	check := func() error {
		res, err := scrape.CheckForChange(ctx, deps, rawURL, opts)
		if err != nil {
			return err
		}
		// Diff-command precedent: silence = unchanged, exit 0 either way.
		if !res.Changed {
			return nil
		}
		fmt.Printf("changed=true old=%s new=%s\n%s", res.OldHash, res.NewHash, res.Diff)
		if res.WebhookStatus != "" && res.WebhookStatus != "sent" {
			fmt.Fprintf(os.Stderr, "watch: webhook %s\n", res.WebhookStatus)
		}
		return nil
	}

	if o.Once {
		return check()
	}
	// Immediate first check, then ticker; Ctrl-C exits 0.
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt)
	defer stop()
	if err := check(); err != nil {
		return err
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := check(); err != nil {
				return err
			}
		}
	}
}
