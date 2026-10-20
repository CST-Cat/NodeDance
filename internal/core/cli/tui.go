package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
	"unicode"

	"github.com/CST-Cat/NodeDance/internal/control"
	"github.com/CST-Cat/NodeDance/internal/core/agents"
	"github.com/CST-Cat/NodeDance/internal/i18n"
	"golang.org/x/term"
)

type terminalIO struct {
	io.Reader
	io.Writer
}

// safeScreen keeps server data and error text from injecting terminal escapes.
func safeScreen(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return -1
		}
		return r
	}, s)
}
func runTUI(ctx context.Context, args []string, lookup LookupEnv, stdin io.Reader, stdout, stderr io.Writer, version string) error {
	flags := flag.NewFlagSet("nodedance tui", flag.ContinueOnError)
	flags.SetOutput(stderr)
	connection := addConnectionFlags(flags, lookup)
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected TUI arguments")
	}
	file, ok := stdin.(*os.File)
	if !ok || !term.IsTerminal(int(file.Fd())) {
		return errors.New("TUI requires a terminal; use nodedance operations and nodedance call for automation")
	}
	state, err := term.MakeRaw(int(file.Fd()))
	if err != nil {
		return err
	}
	defer term.Restore(int(file.Fd()), state)
	fmt.Fprint(stdout, "\x1b[?1049h")
	defer fmt.Fprint(stdout, "\x1b[?1049l")
	screen := term.NewTerminal(terminalIO{stdin, stdout}, "nodedance> ")
	if width, height, err := term.GetSize(int(file.Fd())); err == nil {
		screen.SetSize(width, height)
	}
	language := i18n.From(ctx)
	tr := func(s string) string { return language.Text(s) }
	write := func(s string) { fmt.Fprint(screen, safeScreen(tr(s))) }
	prompt := func(label string, secret bool) (string, error) {
		label = tr(label)
		screen.SetPrompt(label + ": ")
		if secret {
			return screen.ReadPassword(label + ": ")
		}
		return screen.ReadLine()
	}
	client, err := connection.client()
	if err != nil {
		return err
	}
	defer client.Close()
	catalogCtx, catalogCancel := context.WithTimeout(ctx, 15*time.Second)
	catalog, err := client.Catalog(catalogCtx)
	catalogCancel()
	if err != nil {
		write(tr("Core catalog unavailable; local maintenance remains available.\n") + err.Error() + "\n")
		catalog = control.Catalog{Version: 1}
	}
	operations := terminalOperations(catalog)
	filter := ""
	page := 0
	params := map[string]string{}
	query := map[string]string{}
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		selected := []control.Operation{}
		for _, o := range operations {
			o.Description = tr(o.Description)
			if strings.Contains(strings.ToLower(o.Name+" "+o.Description), strings.ToLower(filter)) {
				selected = append(selected, o)
			}
		}
		pageSize := 12
		if _, height, e := term.GetSize(int(file.Fd())); e == nil && height > 10 {
			pageSize = max(3, height-8)
		}
		maxPage := max(0, (len(selected)-1)/pageSize)
		page = min(page, maxPage)
		fmt.Fprint(screen, "\x1b[2J\x1b[H")
		write(fmt.Sprintf(tr("NodeDance %s | %s\nFilter: %s | Page %d/%d\n\n"), version, *connection.server, filter, page+1, maxPage+1))
		start := page * pageSize
		end := min(len(selected), start+pageSize)
		for i := start; i < end; i++ {
			write(fmt.Sprintf("%2d  %-27s %s\n", i-start+1, selected[i].Name, selected[i].Description))
		}
		write("\nNumber: open operation | /text: search | n/p: page | r: refresh Core | l: language | q: quit\n")
		choice, err := prompt("Select", false)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		choice = strings.TrimSpace(choice)
		switch choice {
		case "r":
			refreshCtx, stop := context.WithTimeout(ctx, 15*time.Second)
			refreshed, e := client.Catalog(refreshCtx)
			stop()
			if e == nil {
				catalog = refreshed
				operations = terminalOperations(catalog)
			} else {
				write(e.Error() + "\n")
				prompt("Enter to return", false)
			}
			continue
		case "q", "quit":
			return nil
		case "l":
			if language == i18n.English {
				language = i18n.SimplifiedChinese
			} else {
				language = i18n.English
			}
			ctx = i18n.WithLocale(ctx, language)
			continue
		case "n":
			page = min(page+1, maxPage)
			continue
		case "p":
			page = max(0, page-1)
			continue
		}
		if strings.HasPrefix(choice, "/") {
			filter = strings.TrimPrefix(choice, "/")
			page = 0
			continue
		}
		var number int
		if _, err = fmt.Sscanf(choice, "%d", &number); err != nil || number < 1 || start+number > end {
			continue
		}
		o := selected[start+number-1]
		fmt.Fprint(screen, "\x1b[2J\x1b[H")
		write(o.Name + "\n" + o.Description + "\n\n")
		if o.Transport == "local" {
			value, err := prompt("Arguments as JSON array (or @file); blank returns", false)
			if err != nil {
				return err
			}
			if value == "" {
				continue
			}
			raw, err := readValue(value, stdin)
			var localArgs []string
			if err == nil {
				err = json.Unmarshal(raw, &localArgs)
			}
			if err == nil {
				var tokenInput io.Reader = stdin
				if o.Name == "client.update" && updateUsesRemote(localArgs, lookup) {
					// Explicit remote workflows reuse this TUI's authenticated connection.
					// A plain local update never inherits a remote server or session.
					for _, item := range []struct{ name, value string }{
						{"server", *connection.server}, {"origin", *connection.origin},
						{"state-dir", *connection.stateDir}, {"ca-file", *connection.caFile},
						{"timeout", connection.timeout.String()},
					} {
						present := false
						for _, arg := range localArgs {
							arg = strings.TrimLeft(arg, "-")
							if arg == item.name || strings.HasPrefix(arg, item.name+"=") {
								present = true
							}
						}
						if !present {
							localArgs = append(localArgs, "--"+item.name, item.value)
						}
					}
					if *connection.dev {
						localArgs = append(localArgs, "--dev")
					}
				}
				if o.Name == "agent.enroll" {
					token, e := prompt("Enrollment token (hidden)", true)
					if e != nil {
						return e
					}
					tokenInput = strings.NewReader(token + "\n")
					hasStdin := false
					for _, a := range localArgs {
						if a == "--token-stdin" {
							hasStdin = true
						}
					}
					if !hasStdin {
						localArgs = append(localArgs, "--token-stdin")
					}
				}
				// Local processes use the same CLI dispatch. Restore cooked mode while
				// they run so Ctrl-C and service output behave normally.
				term.Restore(int(file.Fd()), state)
				err = runControl(ctx, o.Name, localArgs, lookup, tokenInput, stdout, stderr, version)
				_, rawErr := term.MakeRaw(int(file.Fd()))
				if rawErr != nil {
					return rawErr
				}
			}
			if err != nil {
				write(tr("Error: ") + err.Error() + "\n")
			}
			if _, err = prompt("Enter to return", false); err != nil {
				return err
			}
			continue
		}
		in := control.Input{Params: map[string]string{}, Query: map[string]string{}}
		for _, p := range o.Parameters {
			value, e := prompt(p+" ["+params[p]+"]", false)
			if e != nil {
				return e
			}
			if value == "" {
				value = params[p]
			}
			in.Params[p] = value
			params[p] = value
		}
		for _, p := range o.Query {
			last := query[p]
			if p == "ticket" {
				last = ""
			}
			value, e := prompt(p+" ["+last+"] (blank keeps; - clears)", p == "ticket")
			if e != nil {
				return e
			}
			if value == "" {
				value = last
			}
			if value == "-" {
				value = ""
			}
			if value != "" {
				in.Query[p] = value
			}
			if p != "ticket" {
				query[p] = value
			}
		}
		if o.BodySchema != nil {
			if o.Name == "auth.login" {
				username, e := prompt("Administrator username", false)
				if e != nil {
					return e
				}
				password, e := prompt("Password (hidden)", true)
				if e != nil {
					return e
				}
				in.Body, _ = json.Marshal(map[string]string{"username": username, "password": password})
			} else if o.Name == "agents.enrollment" {
				in.Body, err = promptAgentEnrollment(prompt, firstNonEmpty(*connection.origin, *connection.server))
				if err != nil {
					return err
				}
			} else {
				raw, _ := json.MarshalIndent(o.BodySchema, "", "  ")
				write(tr("Body schema:\n") + string(raw) + "\n")
				value, e := prompt("JSON body or @private-file (input hidden)", true)
				if e != nil {
					return e
				}
				raw, e = readOperationValue(value, strings.NewReader(""), o.MaxBodyBytes)
				if e != nil {
					write(e.Error() + "\n")
					if _, err := prompt("Enter to return", false); err != nil {
						return err
					}
					continue
				} else {
					in.Body = raw
				}
			}
		}
		if o.Idempotent {
			value, e := prompt("Idempotency key (reuse for retries)", false)
			if e != nil {
				return e
			}
			in.IdempotencyKey = value
		}
		for name, description := range o.Headers {
			if name == "Idempotency-Key" || name == "X-File-Version" {
				continue
			}
			field, _ := description.(map[string]any)
			value, e := prompt(name, field["writeOnly"] == true)
			if e != nil {
				return e
			}
			if value != "" {
				if in.Headers == nil {
					in.Headers = map[string]string{}
				}
				in.Headers[name] = value
			}
		}
		wait := false
		if o.Completion != nil {
			value, e := prompt("Wait for completion? (y/N)", false)
			if e != nil {
				return e
			}
			wait = strings.EqualFold(strings.TrimSpace(value), "y")
		}

		var upload io.Reader
		var closeUpload func()
		outputPath := ""
		if o.Transport == "upload" || o.Transport == "multipart" {
			value, e := prompt("Input file or FIFO path; - for inline content", false)
			if e != nil {
				return e
			}
			if value == "-" {
				value, err = prompt("Upload content as JSON string (or @file)", false)
				if err == nil {
					var raw []byte
					raw, err = readValue(value, stdin)
					var content string
					if err == nil {
						err = json.Unmarshal(raw, &content)
					}
					upload = strings.NewReader(content)
					closeUpload = func() {}
				}
			} else {
				upload, closeUpload, err = uploadInput(value, stdin)
			}
			if o.Transport == "upload" {
				value, e = prompt("Expected file version (optional)", false)
				if e != nil {
					if closeUpload != nil {
						closeUpload()
					}
					return e
				}
				in.FileVersion = value
			}
		}
		if o.Transport == "download" {
			outputPath, err = prompt("New output file", false)
		}
		if err != nil {
			write(tr("Error: ") + err.Error() + "\n")
			err = nil
			if closeUpload != nil {
				closeUpload()
			}
			prompt("Enter to return", false)
			continue
		}
		observed := false
		terminalJSONL := false
		var streamInputFile *os.File
		if o.Transport == "terminal" {
			value, e := prompt("Terminal interface (interactive/jsonl; blank uses interactive)", false)
			if e != nil {
				return e
			}
			terminalJSONL = strings.EqualFold(strings.TrimSpace(value), "jsonl")
		}
		if o.Duplex && (o.Transport == "websocket" || terminalJSONL) {
			value, e := prompt("JSONL input file (optional; blank uses line input)", false)
			if e != nil {
				return e
			}
			if value != "" {
				streamInputFile, err = os.Open(value)
				if err != nil {
					write(tr("Error: ") + err.Error() + "\n")
					prompt("Enter to return", false)
					continue
				}
			}
		}
		requestCtx, cancel := context.WithCancel(ctx)
		if *connection.timeout > 0 {
			cancel()
			requestCtx, cancel = context.WithTimeout(ctx, *connection.timeout)
		}
		if o.Transport == "terminal" && !terminalJSONL {
			err = interactiveTerminal(requestCtx, client, in, file, stdout)
			if err != nil {
				write(tr("Terminal: ") + err.Error() + "\n")
			}
		} else if o.Transport == "websocket" || terminalJSONL {
			write("Stream active. Enter /stop to return. Terminal input uses JSONL frames.\n")
			pipeReader, pipeWriter := io.Pipe()
			done := make(chan error, 1)
			var input io.Reader
			if o.Duplex {
				if streamInputFile != nil {
					input = streamInputFile
				} else {
					input = pipeReader
				}
			}
			go func() {
				e := streamJSONL(requestCtx, client, o, in, input, screen)
				pipeReader.Close()
				if e != nil {
					write(tr("Stream: ") + e.Error() + "\n")
				}
				done <- e
			}()
			streamEnded := false
			for {
				line, e := prompt("Stream (/stop)", false)
				if e != nil || line == "/stop" {
					cancel()
					break
				}
				select {
				case <-done:
					streamEnded = true
					goto streamDone
				default:
				}
				if input != nil && streamInputFile == nil {
					if _, e = io.WriteString(pipeWriter, line+"\n"); e != nil {
						break
					}
				}
			}
		streamDone:
			cancel()
			pipeWriter.Close()
			pipeReader.Close()
			if streamInputFile != nil {
				streamInputFile.Close()
			}
			if !streamEnded {
				<-done
			}
		} else {
			result := executeFinite(requestCtx, client, catalog, o, in, upload, outputPath, false, nil)
			if wait && result.Error == "" {
				observed = true
				accepted, _ := json.Marshal(result)
				write(string(accepted) + "\n")
				done := make(chan control.Result, 1)
				go func() {
					final := client.Wait(requestCtx, catalog, o, in, result, func(update control.Result) { raw, _ := json.Marshal(update); write(string(raw) + "\n") })
					if final.Error != "" {
						write(tr("Error: ") + final.Error + "\n")
					}
					done <- final
					write("Observation finished. Press Enter to return.\n")
				}()
				for {
					value, e := prompt("Observe (/stop)", false)
					if e != nil || value == "/stop" {
						cancel()
						result = <-done
						break
					}
					select {
					case result = <-done:
						goto observationDone
					default:
					}
				}
			}
		observationDone:
			raw, _ := json.MarshalIndent(result, "", "  ")
			// Paginate large results so history and inventory remain inspectable.
			lines := strings.Split(string(raw), "\n")
			for i := 0; i < len(lines); i += 18 {
				write(strings.Join(lines[i:min(len(lines), i+18)], "\n") + "\n")
				if i+18 < len(lines) {
					v, e := prompt("Enter: more, q: stop", false)
					if e != nil {
						cancel()
						return e
					}
					if v == "q" {
						break
					}
				}
			}
			// Never echo a request body: it can contain passwords and private keys.
		}
		cancel()
		if closeUpload != nil {
			closeUpload()
		}
		if observed {
			continue
		}
		if _, err = prompt("Enter to return", false); err != nil {
			return err
		}
	}
}

func promptAgentEnrollment(prompt func(string, bool) (string, error), server string) ([]byte, error) {
	name, err := prompt("Node display name", false)
	if err != nil {
		return nil, err
	}
	coreURL, err := prompt("Core URL reachable from Agent (blank uses server)", false)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(coreURL) == "" {
		coreURL = server
	}
	for {
		method, err := prompt("Deployment method (native/docker; blank uses native)", false)
		if err != nil {
			return nil, err
		}
		method = strings.TrimSpace(method)
		if method == "" {
			method = agents.DefaultDeploymentMethod
		}
		if !agents.ValidDeploymentMethod(method) {
			continue
		}
		return json.Marshal(agents.EnrollmentRequest{DisplayName: strings.TrimSpace(name), CoreURL: strings.TrimSpace(coreURL), DeploymentMethod: method})
	}
}
