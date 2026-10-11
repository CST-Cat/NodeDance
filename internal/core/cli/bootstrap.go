package cli

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/CST-Cat/NodeDance/internal/i18n"
	"golang.org/x/term"
)

func promptInitialAdministrator(stdin io.Reader, stdout io.Writer) (string, string, error) {
	tty, ok := stdin.(*os.File)
	if !ok || !term.IsTerminal(int(tty.Fd())) {
		return "", "", nil
	}
	state, err := term.MakeRaw(int(tty.Fd()))
	if err != nil {
		return "", "", err
	}
	defer term.Restore(int(tty.Fd()), state)
	screen := term.NewTerminal(terminalIO{stdin, stdout}, "")
	fmt.Fprint(screen, i18n.TextFor(stdout, "First deployment: leave each field blank and press Enter to generate secure random credentials.\n"))
	screen.SetPrompt(i18n.TextFor(stdout, "Administrator username (Enter generates): "))
	username, err := screen.ReadLine()
	if err != nil {
		return "", "", err
	}
	password, err := screen.ReadPassword(i18n.TextFor(stdout, "Administrator password (hidden; Enter generates): "))
	if err != nil {
		return "", "", err
	}
	return strings.TrimSpace(username), password, nil
}
