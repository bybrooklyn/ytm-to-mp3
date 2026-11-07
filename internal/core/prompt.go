package core

import (
	"context"
	"errors"

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
func (AutoPrompter) Notice(string, string)                                     {}
func (AutoPrompter) Working(ctx context.Context, _ string, f func(context.Context) error) error {
	return f(ctx)
}
