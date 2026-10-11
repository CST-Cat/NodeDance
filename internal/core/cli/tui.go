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
	"unicode"

	"github.com/CST-Cat/NodeDance/internal/control"
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
	operations := allOperations()
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
		write("\nNumber: open operation | /text: search | n/p: page | l: language | q: quit\n")
		choice, err := prompt("Select", false)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		choice = strings.TrimSpace(choice)
		switch choice {
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
				var tokenInput io.Reader = strings.NewReader("")
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
			} else {
				raw, _ := json.MarshalIndent(o.BodySchema, "", "  ")
				write(tr("Body schema:\n") + string(raw) + "\n")
				value, e := prompt("JSON body or @private-file (input hidden)", true)
				if e != nil {
					return e
				}
				raw, e = readValue(value, strings.NewReader(""))
				if e != nil {
					write(e.Error() + "\n")
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
		var upload io.Reader
		var uploadFile *os.File
		var download *os.File
		outputPath := ""
		if o.Transport == "upload" || o.Transport == "multipart" {
			value, e := prompt("Input file", false)
			if e != nil {
				return e
			}
			uploadFile, err = os.Open(value)
			if err == nil {
				upload = uploadFile
			}
			if o.Transport == "upload" {
				value, e = prompt("Expected file version (optional)", false)
				if e != nil {
					if uploadFile != nil {
						uploadFile.Close()
					}
					return e
				}
				in.FileVersion = value
			}
		}
		if o.Transport == "download" {
			outputPath, err = prompt("New output file", false)
			if err == nil {
				download, err = os.OpenFile(outputPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
			}
		}
		if err != nil {
			write(tr("Error: ") + err.Error() + "\n")
			err = nil
			if uploadFile != nil {
				uploadFile.Close()
			}
			prompt("Enter to return", false)
			continue
		}
		requestCtx, cancel := context.WithCancel(ctx)
		if *connection.timeout > 0 {
			cancel()
			requestCtx, cancel = context.WithTimeout(ctx, *connection.timeout)
		}
		if o.Transport == "terminal" {
			err = interactiveTerminal(requestCtx, client, in, file, stdout)
			if err != nil {
				write(tr("Terminal: ") + err.Error() + "\n")
			}
		} else if o.Transport == "websocket" {
			write("Stream active. Enter /stop to return. Terminal input uses JSONL frames.\n")
			pipeReader, pipeWriter := io.Pipe()
			done := make(chan error, 1)
			var input io.Reader
			if o.Name == "terminal.connect" {
				input = pipeReader
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
				if input != nil {
					if _, e = io.WriteString(pipeWriter, line+"\n"); e != nil {
						break
					}
				}
			}
		streamDone:
			cancel()
			pipeWriter.Close()
			pipeReader.Close()
			if !streamEnded {
				<-done
			}
		} else {
			var writer io.Writer
			if download != nil {
				writer = download
			}
			result := client.Execute(requestCtx, o, in, upload, writer)
			if download != nil {
				if result.Error == "" {
					if e := download.Sync(); e != nil {
						result.Error = e.Error()
					}
				}
				if e := download.Close(); e != nil {
					result.Error = e.Error()
				}
				if result.Error != "" {
					os.Remove(outputPath)
				} else {
					result.File = outputPath
				}
			}
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
		if uploadFile != nil {
			uploadFile.Close()
		}
		if _, err = prompt("Enter to return", false); err != nil {
			return err
		}
	}
}
