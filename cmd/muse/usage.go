package main

import (
	"fmt"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/ellistarn/muse/internal/runlog"
)

func newUsageCmd() *cobra.Command {
	var days int
	var detail bool
	cmd := &cobra.Command{
		Use:   "usage",
		Short: "Show API usage and cost summary",
		Long: `Reads the local run log and prints a summary of API usage grouped by command.
Defaults to the last 30 days.`,
		Example: `  muse usage              # last 30 days
  muse usage --days 7     # last week
  muse usage --detail     # show each run individually`,
		RunE: func(cmd *cobra.Command, args []string) error {
			path := filepath.Join(museDir(), "runs.jsonl")
			since := time.Now().AddDate(0, 0, -days)
			records, err := runlog.Read(path, since)
			if err != nil {
				return fmt.Errorf("failed to read run log: %w", err)
			}
			if len(records) == 0 {
				fmt.Fprintf(cmd.OutOrStdout(), "No API usage in the last %d days.\n", days)
				return nil
			}

			if detail {
				fmt.Fprintf(cmd.OutOrStdout(), "API usage (last %d days):\n\n", days)
				for _, r := range records {
					fmt.Fprintf(cmd.OutOrStdout(), "  %s  %-12s %6dk in  %5dk out  $%.2f",
						r.Timestamp.Format("2006-01-02 15:04"),
						r.Command,
						r.InputTokens/1000,
						r.OutputTokens/1000,
						r.Cost)
					if r.Cached {
						fmt.Fprintf(cmd.OutOrStdout(), "  (cached)")
					}
					fmt.Fprintln(cmd.OutOrStdout())
				}
				fmt.Fprintln(cmd.OutOrStdout())
			}

			summaries := runlog.Summary(records)
			if !detail {
				fmt.Fprintf(cmd.OutOrStdout(), "API usage (last %d days):\n\n", days)
			}
			var totalRuns, totalIn, totalOut int
			var totalCost float64
			for _, s := range summaries {
				fmt.Fprintf(cmd.OutOrStdout(), "  %-14s %3d runs  %5dk in  %5dk out  $%.2f\n",
					s.Command, s.Runs, s.InputTokens/1000, s.OutputTokens/1000, s.Cost)
				totalRuns += s.Runs
				totalIn += s.InputTokens
				totalOut += s.OutputTokens
				totalCost += s.Cost
			}
			fmt.Fprintf(cmd.OutOrStdout(), "\n  %-14s %3d runs  %5dk in  %5dk out  $%.2f\n",
				"total", totalRuns, totalIn/1000, totalOut/1000, totalCost)
			return nil
		},
	}
	cmd.Flags().IntVar(&days, "days", 30, "number of days to include")
	cmd.Flags().BoolVar(&detail, "detail", false, "show individual runs")
	return cmd
}
