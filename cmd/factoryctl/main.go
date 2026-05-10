// Command factoryctl is the factory CLI admin tool.
//
// It communicates with the admin API server to inspect and manage queues,
// work items, and workers.
//
// Usage:
//
//	factoryctl queues                          List all queues
//	factoryctl queues <name>                   Show queue details
//	factoryctl items <queue>                   List items in a queue
//	factoryctl items <queue> <key>             Show item details + history
//	factoryctl items <queue> -s pending        List items filtered by status
//	factoryctl retry <queue> <key>             Retry a failed/dead-lettered item
//	factoryctl cancel <queue> <key>            Cancel a pending/running item
//	factoryctl purge <queue>                   Purge dead-lettered items
//	factoryctl workers                         List all workers
//	factoryctl workers -q <queue>              List workers for a queue
//	factoryctl events <queue>                  Stream real-time events (SSE)
//
// Environment variables:
//
//	FACTORY_API  - Admin API endpoint (default: http://localhost:8080)
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/hummingbird-org/factory-workqueue/internal/envutil"
)

var apiEndpoint string

func main() {
	apiEndpoint = envutil.Or("FACTORY_API", "http://localhost:8080")

	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}

	var err error
	switch os.Args[1] {
	case "queues":
		err = cmdQueues(os.Args[2:])
	case "items":
		err = cmdItems(os.Args[2:])
	case "retry":
		err = cmdRetry(os.Args[2:])
	case "cancel":
		err = cmdCancel(os.Args[2:])
	case "purge":
		err = cmdPurge(os.Args[2:])
	case "workers":
		err = cmdWorkers(os.Args[2:])
	case "events":
		err = cmdEvents(os.Args[2:])
	case "pause":
		err = cmdPause(os.Args[2:])
	case "resume":
		err = cmdResume(os.Args[2:])
	case "help", "-h", "--help":
		usage()
		return
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", os.Args[1])
		usage()
		os.Exit(1)
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func cmdQueues(args []string) error {
	if len(args) > 0 {
		// Show single queue.
		return getJSON("/admin/queues/"+args[0], func(body []byte) error {
			var q map[string]any
			json.Unmarshal(body, &q)
			prettyPrint(q)
			return nil
		})
	}

	return getJSON("/admin/queues", func(body []byte) error {
		var queues []map[string]any
		json.Unmarshal(body, &queues)

		w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "QUEUE\tBACKEND\tCONCURRENCY\tIN-PROGRESS\tPENDING\tFAILED\tDEAD")
		for _, q := range queues {
			counts, _ := q["counts"].(map[string]any)
			fmt.Fprintf(w, "%s\t%s\t%v\t%v\t%v\t%v\t%v\n",
				q["name"],
				q["compute_backend"],
				q["max_concurrency"],
				q["in_progress"],
				intOr(counts, "pending", 0),
				intOr(counts, "failed", 0),
				intOr(counts, "dead_letter", 0),
			)
		}
		return w.Flush()
	})
}

func cmdItems(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: factoryctl items <queue> [key] [-s status]")
	}
	queue := args[0]

	// Check for specific key.
	if len(args) >= 2 && !strings.HasPrefix(args[1], "-") {
		return getJSON("/admin/queues/"+queue+"/items/"+args[1], func(body []byte) error {
			var detail map[string]any
			json.Unmarshal(body, &detail)
			prettyPrint(detail)
			return nil
		})
	}

	// List items with optional status filter.
	path := "/admin/queues/" + queue + "/items"
	for i := 1; i < len(args)-1; i++ {
		if args[i] == "-s" {
			path += "?status=" + args[i+1]
			break
		}
	}

	return getJSON(path, func(body []byte) error {
		var items []map[string]any
		json.Unmarshal(body, &items)

		w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "KEY\tSTATUS\tPRIORITY\tATTEMPTS\tCREATED")
		for _, item := range items {
			created := ""
			if t, ok := item["created_at"].(string); ok {
				if parsed, err := time.Parse(time.RFC3339Nano, t); err == nil {
					created = time.Since(parsed).Truncate(time.Second).String() + " ago"
				}
			}
			fmt.Fprintf(w, "%s\t%s\t%v\t%v\t%s\n",
				item["key"], item["status"], item["priority"], item["attempts"], created,
			)
		}
		return w.Flush()
	})
}

func cmdRetry(args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("usage: factoryctl retry <queue> <key>")
	}
	return postJSON("/admin/queues/"+args[0]+"/items/"+args[1]+"/retry", nil, func(body []byte) error {
		fmt.Printf("retried %s/%s\n", args[0], args[1])
		return nil
	})
}

func cmdCancel(args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("usage: factoryctl cancel <queue> <key>")
	}
	return postJSON("/admin/queues/"+args[0]+"/items/"+args[1]+"/cancel", nil, func(body []byte) error {
		fmt.Printf("cancelled %s/%s\n", args[0], args[1])
		return nil
	})
}

func cmdPurge(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: factoryctl purge <queue>")
	}
	req, _ := http.NewRequest(http.MethodDelete, apiEndpoint+"/admin/queues/"+args[0]+"/dead-letters", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return fmt.Errorf("http %d: %s", resp.StatusCode, body)
	}
	var result map[string]any
	json.Unmarshal(body, &result)
	fmt.Printf("purged %v dead-lettered items from %s\n", result["count"], args[0])
	return nil
}

func cmdWorkers(args []string) error {
	path := "/admin/workers"
	for i := 0; i < len(args)-1; i++ {
		if args[i] == "-q" {
			path += "?queue=" + args[i+1]
			break
		}
	}

	return getJSON(path, func(body []byte) error {
		var workers []map[string]any
		json.Unmarshal(body, &workers)

		w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "WORKER\tQUEUE\tBACKEND\tSTATUS\tPROCESSED\tLAST HEARTBEAT")
		for _, wk := range workers {
			hb := ""
			if t, ok := wk["last_heartbeat"].(string); ok {
				if parsed, err := time.Parse(time.RFC3339Nano, t); err == nil {
					hb = time.Since(parsed).Truncate(time.Second).String() + " ago"
				}
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%v\t%s\n",
				wk["worker_id"], wk["queue"], wk["compute_backend"],
				wk["status"], wk["items_processed"], hb,
			)
		}
		return w.Flush()
	})
}

func cmdEvents(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: factoryctl events <queue>")
	}

	resp, err := http.Get(apiEndpoint + "/admin/queues/" + args[0] + "/events")
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	fmt.Printf("streaming events for queue %q (Ctrl+C to stop)...\n", args[0])
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data: ") {
			data := strings.TrimPrefix(line, "data: ")
			var event map[string]any
			if json.Unmarshal([]byte(data), &event) == nil {
				fmt.Printf("[%s] key=%s status=%s priority=%v\n",
					time.Now().Format("15:04:05"),
					event["key"], event["status"], event["priority"],
				)
			}
		}
	}
	return scanner.Err()
}

func cmdPause(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: factoryctl pause <queue>")
	}
	resp, err := http.Post(apiEndpoint+"/admin/queues/"+args[0]+"/pause", "application/json", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("http %d: %s", resp.StatusCode, body)
	}
	fmt.Printf("queue %s paused\n", args[0])
	return nil
}

func cmdResume(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: factoryctl resume <queue>")
	}
	resp, err := http.Post(apiEndpoint+"/admin/queues/"+args[0]+"/resume", "application/json", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("http %d: %s", resp.StatusCode, body)
	}
	fmt.Printf("queue %s resumed\n", args[0])
	return nil
}

func getJSON(path string, handler func([]byte) error) error {
	resp, err := http.Get(apiEndpoint + path)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return fmt.Errorf("http %d: %s", resp.StatusCode, body)
	}
	return handler(body)
}

func postJSON(path string, payload any, handler func([]byte) error) error {
	resp, err := http.Post(apiEndpoint+path, "application/json", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return fmt.Errorf("http %d: %s", resp.StatusCode, body)
	}
	return handler(body)
}

func prettyPrint(v any) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.Encode(v)
}

func intOr(m map[string]any, key string, fallback int) int {
	if m == nil {
		return fallback
	}
	if v, ok := m[key]; ok {
		switch n := v.(type) {
		case float64:
			return int(n)
		case int:
			return n
		}
	}
	return fallback
}

func usage() {
	fmt.Fprintf(os.Stderr, `factoryctl — factory work queue admin CLI

Usage:
  factoryctl queues                        List all queues
  factoryctl queues <name>                 Show queue details
  factoryctl items <queue>                 List items in a queue
  factoryctl items <queue> <key>           Show item details + history
  factoryctl items <queue> -s <status>     List items filtered by status
  factoryctl retry <queue> <key>           Retry a failed/dead-lettered item
  factoryctl cancel <queue> <key>          Cancel an item
  factoryctl purge <queue>                 Purge dead-lettered items
  factoryctl workers                       List all workers
  factoryctl workers -q <queue>            List workers for a queue
  factoryctl pause <queue>                 Pause a queue (items enqueue but don't dispatch)
  factoryctl resume <queue>                Resume a paused queue
  factoryctl events <queue>                Stream real-time events

Environment:
  FACTORY_API    Admin API endpoint (default: http://localhost:8080)
`)
}
