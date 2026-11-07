package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync/atomic"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"
	"charm.land/huh/v2/spinner"
	"github.com/charmbracelet/x/term"

	"github.com/bybrooklyn/mfetchy/internal/screen"
)

// checkFolder makes sure the music folder can be created and written, so a typo shows up
// here instead of after a long download.
func checkFolder(s string) error { return screen.CheckFolder(s) }

func one(ctx context.Context, f huh.Field) error {
	return runForm(ctx, huh.NewForm(huh.NewGroup(f)))
}

// errQuit is Ctrl-C in a form: leave mfetchy (quietly, like any interrupt).
var errQuit = fmt.Errorf("quit: %w", context.Canceled)

// formKeys are the form keys: Esc goes back a screen, Ctrl-C quits. The menus are short,
// so typing to filter them is off (and its "/ filter" hint gives way to "esc back").
func formKeys() *huh.KeyMap {
	km := huh.NewDefaultKeyMap()
	km.Quit = key.NewBinding(key.WithKeys("ctrl+c", "esc"))
	// The form handles Quit before the field sees the key, so this binding never filters;
	// it is only the hint in the help line.
	km.Select.Filter = key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back"))
	return km
}

// runForm runs a form with mfetchy's keys. Esc comes back as errBack (the caller returns to
// where it came from), Ctrl-C as errQuit.
func runForm(ctx context.Context, f *huh.Form) error {
	var esc atomic.Bool
	err := f.WithKeyMap(formKeys()).WithTheme(ytmTheme).WithProgramOptions(tea.WithFilter(func(_ tea.Model, msg tea.Msg) tea.Msg {
		if k, ok := msg.(tea.KeyPressMsg); ok {
			esc.Store(k.String() == "esc")
		}
		return msg
	})).RunWithContext(ctx)
	if errors.Is(err, huh.ErrUserAborted) {
		if esc.Load() {
			return errBack
		}
		return errQuit
	}
	return err
}

func working(ctx context.Context, title string, f func(context.Context) error) error {
	if !term.IsTerminal(os.Stdin.Fd()) || !term.IsTerminal(os.Stdout.Fd()) {
		return f(ctx) // no terminal to draw a spinner on (piped, CI, a script)
	}
	var err error
	if serr := spinner.New().WithTheme(ytmSpinner).Title(title).Context(ctx).ActionWithErr(func(c context.Context) error {
		err = f(c)
		return nil
	}).Run(); serr != nil && err == nil {
		err = serr
	}
	return err
}
