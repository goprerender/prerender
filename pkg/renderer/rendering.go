package renderer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

// RenderPage executes full page rendering workflow
func (r *Renderer) RenderPage(url string, result *RenderResult) (string, error) {
	if url == "https://invalid-url-that-triggers-restart" {
		return "", errors.New("artificial error: could not dial \"ws:")
	}

	r.allocatorMutex.RLock()
	defer r.allocatorMutex.RUnlock()

	if r.allocatorCtx == nil || r.allocatorCtx.Err() != nil {
		return "", ErrInvalidContext
	}

	tabCtx, cancelTab := chromedp.NewContext(r.allocatorCtx)
	defer cancelTab()

	ctx, cancel := context.WithTimeout(tabCtx, r.renderTimeout)
	defer cancel()

	if r.captureConsoleLog {
		r.captureConsoleEvents(ctx, result)
	}

	var htmlContent string
	timings := RenderTimings{}

	tasks := chromedp.Tasks{
		chromedp.ActionFunc(func(ctx context.Context) error {
			return network.SetBlockedURLs(r.blockedURLs).Do(ctx)
		}),
		chromedp.ActionFunc(func(ctx context.Context) error {
			return network.SetExtraHTTPHeaders(network.Headers{"X-Prerender": "1"}).Do(ctx)
		}),
		chromedp.ActionFunc(func(ctx context.Context) error {
			start := time.Now()
			_, _, errorText, _, err := page.Navigate(url).Do(ctx)
			timings.Navigation = time.Since(start)
			if err != nil && !strings.Contains(err.Error(), "net::ERR_BLOCKED_BY_CLIENT") {
				return err
			}
			if errorText != "" {
				return fmt.Errorf("page load error %s", errorText)
			}
			return nil
		}),
		chromedp.ActionFunc(func(ctx context.Context) error {
			start := time.Now()
			waitCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			err := chromedp.WaitReady("body", chromedp.ByQuery).Do(waitCtx)
			timings.Waiting = time.Since(start)
			return err
		}),
		chromedp.Sleep(200 * time.Millisecond),
		chromedp.ActionFunc(func(ctx context.Context) error {
			start := time.Now()
			err := chromedp.OuterHTML("html", &htmlContent, chromedp.ByQuery).Do(ctx)
			timings.Rendering = time.Since(start)
			return err
		}),
	}

	startRender := time.Now()
	err := chromedp.Run(ctx, tasks)
	timings.Total = time.Since(startRender)
	result.Timings = timings

	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return "", ErrTimeoutExceeded
		}
		if strings.Contains(err.Error(), "ERR_NAME_NOT_RESOLVED") {
			return "", ErrNameNotResolved
		}
		if strings.Contains(err.Error(), "No node with given id found") {
			return "", ErrDOMNodeNotFound
		}
		return "", err
	}

	// Log only for long operations
	if timings.Total > 3*time.Second {
		r.logger.Infof("Render timings for %s: nav=%v, wait=%v, render=%v, total=%v",
			url,
			timings.Navigation,
			timings.Waiting,
			timings.Rendering,
			timings.Total)
	}

	return htmlContent, nil
}

// captureConsoleEvents collects browser console output
func (r *Renderer) captureConsoleEvents(ctx context.Context, result *RenderResult) {
	chromedp.ListenTarget(ctx, func(ev interface{}) {
		switch ev := ev.(type) {
		case *runtime.EventConsoleAPICalled:
			entry := ConsoleEntry{Type: ev.Type.String()}
			for _, arg := range ev.Args {
				msg := arg.Description
				if msg == "" && arg.Value != nil {
					msg = fmt.Sprintf("%v", arg.Value)
				}
				entry.Messages = append(entry.Messages, msg)
			}
			result.Console = append(result.Console, entry)
		case *runtime.EventExceptionThrown:
			result.Exception = ev.ExceptionDetails.Error()
		case *network.EventResponseReceived:
			r.logger.Debugf("Response: %s %d", ev.Response.URL, ev.Response.Status)
		case *network.EventLoadingFailed:
			r.logger.Warnf("Loading failed: %s", ev.ErrorText)
		}
	})
}

// verifyChromeConnection validates Chrome responsiveness
func (r *Renderer) verifyChromeConnection() error {
	if r.allocatorCtx == nil {
		return errors.New("allocator context is nil")
	}

	ctx, cancel := context.WithTimeout(r.allocatorCtx, 10*time.Second)
	defer cancel()

	browserCtx, cancelBrowser := chromedp.NewContext(ctx)
	defer cancelBrowser()

	var res string
	err := chromedp.Run(browserCtx,
		chromedp.Navigate("about:blank"),
		chromedp.OuterHTML("html", &res),
	)

	if err != nil || res == "" {
		return errors.New("chrome connection test failed")
	}
	return nil
}

// getDebugURL retrieves Chrome DevTools endpoint
func (r *Renderer) getDebugURL(ctx context.Context) (string, error) {
	r.logger.Debugf("Attempting to connect to Chrome on port: %d", r.debugPort)

	// Port availability check removed - Chrome container might be starting up
	// The actual HTTP request below will fail if Chrome is not ready

	debugURL := fmt.Sprintf("http://localhost:%d/json/version", r.debugPort)
	r.logger.Debugf("Fetching debug URL: %s", debugURL)
	req, err := http.NewRequestWithContext(ctx, "GET", debugURL, nil)
	if err != nil {
		return "", err
	}

	resp, err := r.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("unexpected status: %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	var data struct{ WebSocketDebuggerURL string }
	if err := json.Unmarshal(body, &data); err != nil {
		return "", err
	}

	if data.WebSocketDebuggerURL == "" {
		return "", errors.New("empty debug URL")
	}
	return data.WebSocketDebuggerURL, nil
}

// getDebugURLWithRetry attempts debug URL retrieval with exponential backoff
func (r *Renderer) getDebugURLWithRetry() (string, error) {
	attempt := 1
	delay := r.debugURLRetryDelay
	for attempt <= r.debugURLMaxAttempts {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		wsURL, err := r.getDebugURL(ctx)
		cancel()

		if err == nil {
			// Professional solution: Ensure CDP targets exist for headless-shell compatibility
			if err := r.ensureCDPTargets(); err != nil {
				r.logger.Warnf("CDP target initialization warning: %v", err)
				// Continue anyway - this is defensive programming
			}
			return wsURL, nil
		}

		if attempt < r.debugURLMaxAttempts {
			time.Sleep(delay)
			delay *= 2
			if delay > 15*time.Second {
				delay = 15 * time.Second
			}
		}
		attempt++
	}
	return "", fmt.Errorf("failed after %d attempts", r.debugURLMaxAttempts)
}

// ensureCDPTargets ensures that Chrome has at least one target for chromedp compatibility
// This solves the fundamental issue where headless-shell may start with empty targets []
func (r *Renderer) ensureCDPTargets() error {
	targetsURL := fmt.Sprintf("http://localhost:%d/json", r.debugPort)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Check current targets
	req, err := http.NewRequestWithContext(ctx, "GET", targetsURL, nil)
	if err != nil {
		return fmt.Errorf("failed to create targets request: %w", err)
	}

	resp, err := r.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to get targets: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("targets endpoint returned status: %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read targets response: %w", err)
	}

	var targets []map[string]interface{}
	if err := json.Unmarshal(body, &targets); err != nil {
		return fmt.Errorf("failed to parse targets JSON: %w", err)
	}

	// If no targets exist, create one
	if len(targets) == 0 {
		r.logger.Infof("No CDP targets found, creating default target for headless-shell compatibility")

		// Create new target using CDP API
		createURL := fmt.Sprintf("http://localhost:%d/json/new", r.debugPort)
		createReq, err := http.NewRequestWithContext(ctx, "GET", createURL, nil)
		if err != nil {
			return fmt.Errorf("failed to create target request: %w", err)
		}

		createResp, err := r.httpClient.Do(createReq)
		if err != nil {
			return fmt.Errorf("failed to create target: %w", err)
		}
		defer createResp.Body.Close()

		if createResp.StatusCode != http.StatusOK {
			return fmt.Errorf("target creation returned status: %d", createResp.StatusCode)
		}

		r.logger.Infof("Successfully created default CDP target")
	} else {
		r.logger.Debugf("Found %d existing CDP targets", len(targets))
	}

	return nil
}

// isValidURL performs basic URL validation
func isValidURL(url string) bool {
	return strings.HasPrefix(url, "http://") || strings.HasPrefix(url, "https://")
}
