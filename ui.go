package main

import (
	"errors"
	"os"

	"github.com/charmbracelet/huh"
	"golang.org/x/term"
)

// ui shows interactive prompts. When stdin is not a terminal, callers must
// fall back to flags instead of prompting.
type ui struct {
	tty bool
}

func newUI() *ui {
	return &ui{tty: term.IsTerminal(int(os.Stdin.Fd()))}
}

func choose[T comparable](title string, options ...huh.Option[T]) (T, error) {
	var choice T
	err := huh.NewSelect[T]().Title(title).Options(options...).Value(&choice).Run()
	return choice, friendlyAbort(err)
}

func ask(title, defaultValue string) (string, error) {
	answer := defaultValue
	err := huh.NewInput().Title(title).Value(&answer).Validate(huh.ValidateNotEmpty()).Run()
	return answer, friendlyAbort(err)
}

func confirm(title string) (bool, error) {
	var ok bool
	err := huh.NewConfirm().Title(title).Affirmative("Yes").Negative("No").Value(&ok).Run()
	return ok, friendlyAbort(err)
}

func friendlyAbort(err error) error {
	if errors.Is(err, huh.ErrUserAborted) {
		return errors.New("aborted")
	}
	return err
}
