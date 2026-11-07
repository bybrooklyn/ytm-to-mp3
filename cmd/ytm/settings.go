package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"

	tea "charm.land/bubbletea/v2"

	"github.com/bybrooklyn/ytm-to-mp3/internal/config"
	"github.com/bybrooklyn/ytm-to-mp3/internal/screen"
	"github.com/bybrooklyn/ytm-to-mp3/internal/ui"
)

// ytm's palette (internal/ui), under the names the screens here use.
var (
	cDim    = ui.CDim
	cBold   = ui.CBold
	cAccent = ui.CAccent
	cGreen  = ui.CGreen
	cYellow = ui.CYellow
	cReset  = ui.CReset
)

// modelView draws any screen.Model in the terminal: it maps keys to the model and shows what
// the model describes. All the behaviour lives in internal/screen, shared with the desktop app.
type modelView struct{ screen.Model }

func (m *modelView) Init() tea.Cmd { return nil }

func (m *modelView) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.PasteMsg:
		m.Paste(string(msg.Content))
	case tea.KeyPressMsg:
		if m.Editing() && msg.Text != "" {
			m.Type(msg.Text)
		} else {
			m.Key(msg.String())
		}
	}
	if m.Finished() {
		return m, tea.Quit
	}
	return m, nil
}

func (m *modelView) View() tea.View {
	if m.Finished() {
		return tea.NewView("") // leave a clean line behind
	}
	return tea.NewView(m.Screen().ANSI())
}

// runModel shows m until it finishes (left, or an action is due).
func runModel(ctx context.Context, m screen.Model) error {
	_, err := tea.NewProgram(&modelView{m}, tea.WithContext(ctx)).Run()
	return err
}

// printErr reports a failed settings action; backing out of it (Esc, Ctrl-C) isn't one.
func printErr(err error) {
	if err != nil && !errors.Is(err, errBack) && !errors.Is(err, context.Canceled) {
		fmt.Println(ui.Mark(ui.Fail("✗"), ui.Sanitize(err.Error())))
	}
}

// runSettings shows the settings screen. Sign-in/out and the VPN test need the plain
// terminal, so the program quits, the action runs, and the screen comes back where it was.
func runSettings(ctx context.Context, u *ui.UI) error {
	if !u.Interactive() {
		return errors.New("settings need a terminal")
	}
	cur := 0
	landOnAccount := !currentVPN(config.Load()).SignedIn
	for {
		cfg := config.Load()
		v := currentVPN(cfg)
		sm := screen.NewSettings(cfg, v.SignedIn, v.Who, cur, func(c config.Config) error { return c.Save() }, checkFolder)
		sm.SetProvider(providerLabel(providerID(cfg)))
		sm.SetApple(appleSignedIn())
		if landOnAccount {
			sm.SelectAccount() // first run: land on the sign-in row
			landOnAccount = false
		}
		if err := runModel(ctx, sm); err != nil {
			return err
		}
		cur = sm.Cursor()
		if sm.Action() == "" {
			if !config.Exists() {
				if err := sm.Config().Save(); err != nil {
					return fmt.Errorf("couldn't save your settings to %s: %w", config.Path(), err)
				}
			}
			return nil
		}
		if err := doAction(ctx, u, sm.Action()); err != nil {
			return err
		}
	}
}

// runConnections shows every account and source, and runs the sign-in (or change) the
// highlighted one asks for, coming back to the list afterwards.
func runConnections(ctx context.Context, u *ui.UI) error {
	if !u.Interactive() {
		return errors.New("connections need a terminal")
	}
	cur := 0
	for {
		cm := screen.NewConnections(screen.DetectConnections(), cur)
		if err := runModel(ctx, cm); err != nil {
			return err
		}
		cur = cm.Cursor()
		if cm.Action() == "" {
			return nil
		}
		if err := doAction(ctx, u, cm.Action()); err != nil {
			return err
		}
	}
}

// doAction runs the work a screen asked for ("signin", "premium", ...) in the plain terminal.
// Only a real failure (saving settings) is returned; a flow the user backs out of or that
// fails is reported and the screen comes back.
func doAction(ctx context.Context, u *ui.UI, action string) error {
	switch action {
	case "signout":
		// Ends the session at the provider too, not just here. Mullvad's device keys are
		// removed from the account; Proton's stay listed until removed at account.protonvpn.com.
		signOutVPN(ctx, config.Load())
	case "signin":
		c := config.Load()
		if err := vpnFlow(ctx, &c); err != nil {
			printErr(err)
		}
		return c.Save()
	case "premium":
		printErr(premiumSetup(ctx, u))
	case "applein":
		printErr(appleLogin(ctx, u))
	case "appleout":
		printErr(appleLogout(u))
	case "youtube":
		printErr(chooseYouTubeBrowser(ctx))
	case "bandcamp":
		printErr(chooseBandcampBrowser(ctx))
	case "test":
		printErr(runStatus(ctx, u))
		fmt.Print("\n  " + ui.Dim("press enter to go back") + " ")
		_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
	}
	return nil
}
