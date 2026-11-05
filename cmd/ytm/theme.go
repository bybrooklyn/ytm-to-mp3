package main

import (
	"os"

	"charm.land/huh/v2"
	"charm.land/huh/v2/spinner"
	"charm.land/lipgloss/v2"
)

// ytm's look for huh's menus and prompts: the same plain ANSI colours as the settings
// screen and the download display (internal/ui), so everything follows the terminal's own
// palette. Cyan is the accent (titles, the cursor, the chosen button), green what's picked,
// dim the help. NO_COLOR keeps huh's plain base look.
var ytmTheme = huh.ThemeFunc(func(isDark bool) *huh.Styles {
	t := huh.ThemeBase16(isDark)
	if _, off := os.LookupEnv("NO_COLOR"); off {
		return huh.ThemeBase(isDark)
	}
	var (
		accent = lipgloss.Color("6") // cyan
		green  = lipgloss.Color("2")
		dim    = lipgloss.Color("8")
		red    = lipgloss.Color("1")
		text   = lipgloss.Color("7")
	)
	f := &t.Focused
	f.Base = f.Base.BorderForeground(accent)
	f.Card = f.Base
	f.Title = f.Title.Foreground(accent).Bold(true)
	f.NoteTitle = f.NoteTitle.Foreground(accent).Bold(true)
	f.Description = f.Description.Foreground(dim)
	f.ErrorIndicator = f.ErrorIndicator.Foreground(red)
	f.ErrorMessage = f.ErrorMessage.Foreground(red)
	f.SelectSelector = f.SelectSelector.Foreground(accent).SetString("› ")
	f.NextIndicator = f.NextIndicator.Foreground(accent)
	f.PrevIndicator = f.PrevIndicator.Foreground(accent)
	f.Option = f.Option.Foreground(text)
	f.SelectedOption = f.SelectedOption.Foreground(green)
	f.SelectedPrefix = f.SelectedPrefix.Foreground(green)
	f.MultiSelectSelector = f.MultiSelectSelector.Foreground(accent)
	f.FocusedButton = f.FocusedButton.Foreground(lipgloss.Color("0")).Background(accent).Bold(true)
	f.BlurredButton = f.BlurredButton.Foreground(text).Background(lipgloss.Color("0"))
	f.TextInput.Cursor = f.TextInput.Cursor.Foreground(accent)
	f.TextInput.Prompt = f.TextInput.Prompt.Foreground(accent)
	f.TextInput.Placeholder = f.TextInput.Placeholder.Foreground(dim)

	b := &t.Blurred
	*b = *f
	b.Base = b.Base.BorderStyle(lipgloss.HiddenBorder())
	b.Card = b.Base
	b.Title = b.Title.Foreground(dim).Bold(false)
	b.NoteTitle = b.NoteTitle.Foreground(dim).Bold(false)
	b.TextInput.Prompt = b.TextInput.Prompt.Foreground(dim)
	b.NextIndicator = lipgloss.NewStyle()
	b.PrevIndicator = lipgloss.NewStyle()

	t.Group.Title = f.Title
	t.Group.Description = f.Description
	return t
})

// ytmSpinner is the busy spinner in the same colours.
var ytmSpinner = spinner.ThemeFunc(func(bool) *spinner.Styles {
	if _, off := os.LookupEnv("NO_COLOR"); off {
		return &spinner.Styles{Spinner: lipgloss.NewStyle(), Title: lipgloss.NewStyle()}
	}
	return &spinner.Styles{
		Spinner: lipgloss.NewStyle().Foreground(lipgloss.Color("6")),
		Title:   lipgloss.NewStyle(),
	}
})
