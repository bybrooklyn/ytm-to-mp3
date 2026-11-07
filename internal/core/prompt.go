package core

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/bybrooklyn/mfetchy/internal/export"
	"github.com/bybrooklyn/mfetchy/internal/ui"
)

// ErrBack is what a Prompter returns when the person backed out of a question (Esc in the
// terminal, Cancel in a dialog). A flow that gets it returns to where it came from.
var ErrBack = errors.New("back")

// Question is a yes/no question.
type Question struct {
	Title   string // the question
	Detail  string // the longer text under it (may be empty)
	Yes, No string // button labels; empty means the plain "yes"/"no" of a command line
	Default bool   // the answer when it cannot ask, or on empty input
}

// Option is one choice of a Select question.
type Option struct {
	Value string
	Label string
	Hint  string // optional second line
}

// Kinds of field in a FormSpec.
const (
	FieldText   = "text"   // a line of text
	FieldSecret = "secret" // a line of text that is never shown as it is typed (passwords, tokens)
	FieldChoice = "choice" // one of Options
	FieldYesNo  = "yesno"  // a yes/no choice; the answer is "yes" or "no"
)

// Field is one input of a FormSpec.
type Field struct {
	Key     string // the key of the answer
	Label   string
	Detail  string
	Kind    string // one of the Field* constants; "" is FieldText
	Options []Option
	Default string // FieldText: the starting text; FieldChoice: a Value; FieldYesNo: "yes" or "no"
	Yes, No string // FieldYesNo: the labels of the two answers
	// Validate, if set, is asked before the form is accepted; its error is shown next to
	// the field and the form stays open.
	Validate func(string) error
}

// FormSpec is several inputs asked together, so one dialog (or one terminal form) holds
// them: a username and a password, a folder and a mode.
type FormSpec struct {
	Title, Detail string
	Fields        []Field
	Submit        string // the OK button; "" is "OK"
}

// FileRequest asks for a file or a folder on this computer.
type FileRequest struct {
	Title, Detail string
	File, Folder  bool     // what may be chosen (a file, a folder, or either)
	Exts          []string // the file kinds that make sense (".zip", ".conf"), a hint for pickers
	// Validate, if set, is asked before the path is accepted; its error is shown and the
	// question stays open.
	Validate func(string) error
}

// Info is a page of information for the person: a heading, some text, numbered steps, a
// link that opens in their browser and perhaps a code to copy.
type Info struct {
	Title     string
	Detail    string
	Steps     []string
	Link      string // an https address
	LinkLabel string // the button's label ("Open takeout.google.com")
	Code      string // a short code the person types somewhere (Tidal's)
	Waiting   string // while Wait runs: what is being waited for
	// AutoOpen opens Link right away where there is no button to click (the terminal).
	AutoOpen bool
}

// Prompter is how a run asks the person questions: whether to complete albums, whether to
// try the failed songs again, which browser holds a sign-in. The terminal answers on the
// command line or in a form, the window with a dialog. A flow never reads the keyboard or
// opens a form itself. Every method returns when the person answers or ctx ends (then with
// ctx.Err()), or ErrBack when they backed out. A Prompter that cannot ask (no terminal,
// nobody there) returns the default.
type Prompter interface {
	Confirm(ctx context.Context, q Question) (bool, error)
	// Select asks for one of options; def is the Value to preselect.
	Select(ctx context.Context, title, detail string, options []Option, def string) (string, error)
	// Input asks for a line of text; empty input returns def.
	Input(ctx context.Context, title, detail, def string) (string, error)
	// Form asks several fields at once and returns the answers by Field.Key. Secret fields
	// are masked in every Prompter.
	Form(ctx context.Context, f FormSpec) (map[string]string, error)
	// File asks for a path to a file or folder (the window opens a file chooser).
	File(ctx context.Context, r FileRequest) (string, error)
	// Show tells the person something that needs reading and an OK, perhaps with a link.
	Show(ctx context.Context, i Info) error
	// Wait shows i while f runs (the Tidal code; the Takeout steps while the zip is
	// awaited) and lets the person cancel, which ends f's context. It returns f's error.
	Wait(ctx context.Context, i Info, f func(context.Context) error) error
	// Notice tells the person something that needs no answer.
	Notice(title, detail string)
	// Working runs f while showing that something is in progress (a spinner, a busy
	// dialog). It returns f's error.
	Working(ctx context.Context, title string, f func(context.Context) error) error
}

// TerminalPrompter asks on the plain command line through u, exactly as mfetchy always has:
// a yes/no question is one line ending in [Y/n]. It cannot show a Select; that returns the
// default (the terminal program wraps this with its forms).
type TerminalPrompter struct{ U *ui.UI }

func (p TerminalPrompter) Confirm(ctx context.Context, q Question) (bool, error) {
	if ctx.Err() != nil {
		return q.Default, ctx.Err()
	}
	// Detail is for dialogs; on the command line the lines printed before the question
	// already say it.
	return p.U.Confirm(q.Title, q.Default), nil
}

func (p TerminalPrompter) Select(ctx context.Context, title, detail string, options []Option, def string) (string, error) {
	return def, ctx.Err()
}

func (p TerminalPrompter) Input(ctx context.Context, title, detail, def string) (string, error) {
	if !p.U.Interactive() {
		return def, ctx.Err()
	}
	return p.U.Ask(title+" ", def), ctx.Err()
}

// Form asks the fields one after the other on the command line; secrets are read without
// echo. Nobody there: every field answers its default.
func (p TerminalPrompter) Form(ctx context.Context, f FormSpec) (map[string]string, error) {
	out := map[string]string{}
	for _, fd := range f.Fields {
		out[fd.Key] = fd.Default
	}
	if !p.U.Interactive() {
		return out, ctx.Err()
	}
	for _, fd := range f.Fields {
		for {
			if err := ctx.Err(); err != nil {
				return out, err
			}
			var v string
			switch fd.Kind {
			case FieldSecret:
				v = p.U.AskSecret(fd.Label + ": ")
			case FieldYesNo:
				if p.U.Confirm(fd.Label, fd.Default != "no") {
					v = "yes"
				} else {
					v = "no"
				}
			default:
				v = p.U.Ask(fd.Label+": ", fd.Default)
			}
			if fd.Validate != nil {
				if err := fd.Validate(v); err != nil {
					p.U.Bad("%s", err.Error())
					continue
				}
			}
			out[fd.Key] = v
			break
		}
	}
	return out, nil
}

func (p TerminalPrompter) File(ctx context.Context, r FileRequest) (string, error) {
	if !p.U.Interactive() {
		return "", ctx.Err()
	}
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		v := strings.Trim(p.U.Ask(r.Title+" ", ""), `"' `)
		if v == "" {
			return "", ErrBack
		}
		if r.Validate != nil {
			if err := r.Validate(v); err != nil {
				p.U.Bad("%s", err.Error())
				continue
			}
		}
		return v, nil
	}
}

// Show prints the page.
func (p TerminalPrompter) Show(ctx context.Context, i Info) error {
	printInfo(p.U, i)
	return ctx.Err()
}

// Wait prints the page, then waits; Ctrl-C (the context ending) cancels.
func (p TerminalPrompter) Wait(ctx context.Context, i Info, f func(context.Context) error) error {
	printInfo(p.U, i)
	if i.Waiting != "" {
		p.U.Hint("%s · Ctrl-C to cancel", i.Waiting)
	}
	return f(ctx)
}

// printInfo draws an Info the way the terminal always showed these pages: a heading, the
// steps, "Open <link>" and "Code <code>".
func printInfo(u *ui.UI, i Info) {
	if i.Title != "" {
		u.Head("%s", i.Title)
	}
	for n, s := range i.Steps {
		u.Styled(fmt.Sprintf("    %s %s", ui.Cyan(fmt.Sprintf("%d.", n+1)), s))
	}
	if i.Detail != "" && i.Code == "" {
		u.Hint("%s", i.Detail)
	}
	if i.Link != "" {
		if i.AutoOpen {
			if err := export.OpenBrowser(i.Link); err != nil {
				u.Note("Open this page in your browser: %s", i.Link)
			}
		} else {
			u.Styled("  Open  " + ui.Bold(i.Link))
		}
	}
	if i.Code != "" {
		line := "  Code  " + ui.Bold(i.Code)
		if i.Detail != "" {
			line += ui.Dim("  (" + i.Detail + ")")
		}
		u.Styled(line)
	}
}

func (p TerminalPrompter) Notice(title, detail string) {
	p.U.Note("%s", title)
	if detail != "" {
		p.U.Hint("%s", detail)
	}
}

func (p TerminalPrompter) Working(ctx context.Context, _ string, f func(context.Context) error) error {
	return f(ctx)
}

// AutoPrompter answers every question with its default; for runs with nobody to ask.
type AutoPrompter struct{}

func (AutoPrompter) Confirm(_ context.Context, q Question) (bool, error) { return q.Default, nil }
func (AutoPrompter) Select(_ context.Context, _, _ string, _ []Option, def string) (string, error) {
	return def, nil
}
func (AutoPrompter) Input(_ context.Context, _, _, def string) (string, error) { return def, nil }
func (AutoPrompter) Form(_ context.Context, f FormSpec) (map[string]string, error) {
	out := map[string]string{}
	for _, fd := range f.Fields {
		out[fd.Key] = fd.Default
	}
	return out, nil
}
func (AutoPrompter) File(context.Context, FileRequest) (string, error) { return "", ErrBack }
func (AutoPrompter) Show(context.Context, Info) error                  { return nil }
func (AutoPrompter) Wait(ctx context.Context, _ Info, f func(context.Context) error) error {
	return f(ctx)
}
func (AutoPrompter) Notice(string, string) {}
func (AutoPrompter) Working(ctx context.Context, _ string, f func(context.Context) error) error {
	return f(ctx)
}
