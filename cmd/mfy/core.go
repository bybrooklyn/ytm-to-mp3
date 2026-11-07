package main

import (
	"context"
	"strings"

	"charm.land/huh/v2"

	"github.com/bybrooklyn/mfetchy/internal/core"
	"github.com/bybrooklyn/mfetchy/internal/ui"
)

// errBack means "go back to the menu" from a sub-flow (Esc in a form).
var errBack = core.ErrBack

func init() { core.Version = version }

// core is the options as the pipeline in internal/core takes them.
func (o opts) core() core.Options {
	return core.Options{
		Out: o.out, Format: o.format, Jobs: o.jobs, DryRun: o.dryRun, Retry: o.retry, All: o.all,
		Yes: o.yes, Offline: o.offline, NoVPN: o.noVPN, Conns: o.conns,
		Albums: o.albums, AlbumsMin: o.albumsMin, Song: o.song,
		PremiumUpgrade: o.premiumUpgrade, PremiumLimit: o.premiumLimit,
		TagAfter: o.tagAfter, PremiumAfter: o.premiumAfter, LyricsAfter: o.lyricsAfter,
		Replace: o.replace, Maybe: o.maybe, Source: o.source, Preset: o.preset, Given: o.given,
		Apply: o.apply, Rename: o.rename, Undo: o.undo,
	}
}

// termPrompter answers a run's questions in the terminal: a plain yes/no is one line, a
// labelled question or a choice is a form, and a long wait shows a spinner.
type termPrompter struct{ core.TerminalPrompter }

func newPrompter(u *ui.UI) core.Prompter { return termPrompter{core.TerminalPrompter{U: u}} }

func (p termPrompter) Confirm(ctx context.Context, q core.Question) (bool, error) {
	if q.Yes == "" && q.No == "" {
		return p.TerminalPrompter.Confirm(ctx, q)
	}
	ok := q.Default
	c := huh.NewConfirm().Title(q.Title).Value(&ok)
	if q.Detail != "" {
		c = c.Description(q.Detail)
	}
	if q.Yes != "" {
		c = c.Affirmative(q.Yes)
	}
	if q.No != "" {
		c = c.Negative(q.No)
	}
	err := one(ctx, c)
	return ok, err
}

func (p termPrompter) Select(ctx context.Context, title, detail string, options []core.Option, def string) (string, error) {
	pick := def
	if err := one(ctx, huh.NewSelect[string]().Title(title).Description(detail).Options(huhOptions(options)...).Value(&pick)); err != nil {
		return def, err
	}
	return pick, nil
}

// SelectBoth asks two choices in one form (the Apple Music match questions).
func (p termPrompter) SelectBoth(ctx context.Context, t1 string, o1 []core.Option, d1 string, t2 string, o2 []core.Option, d2 string) (string, string, error) {
	a, b := d1, d2
	err := runForm(ctx, huh.NewForm(huh.NewGroup(
		huh.NewSelect[string]().Title(t1).Options(huhOptions(o1)...).Value(&a),
		huh.NewSelect[string]().Title(t2).Options(huhOptions(o2)...).Value(&b),
	)))
	return a, b, err
}

func (p termPrompter) Input(ctx context.Context, title, detail, def string) (string, error) {
	if !p.U.Interactive() {
		return def, nil
	}
	v := def
	in := huh.NewInput().Title(title).Value(&v)
	if detail != "" {
		in = in.Description(detail)
	}
	if err := one(ctx, in); err != nil {
		return def, err
	}
	return v, nil
}

// Form asks the fields in one form; secrets are masked.
func (p termPrompter) Form(ctx context.Context, spec core.FormSpec) (map[string]string, error) {
	out := map[string]string{}
	for _, fd := range spec.Fields {
		out[fd.Key] = fd.Default
	}
	if !p.U.Interactive() {
		return out, nil
	}
	vals := make([]string, len(spec.Fields))
	yes := make([]bool, len(spec.Fields))
	var fields []huh.Field
	if spec.Title != "" || spec.Detail != "" {
		fields = append(fields, huh.NewNote().Title(spec.Title).Description(spec.Detail))
	}
	for i, fd := range spec.Fields {
		vals[i] = fd.Default
		switch fd.Kind {
		case core.FieldChoice:
			fields = append(fields, huh.NewSelect[string]().Title(fd.Label).Description(fd.Detail).Options(huhOptions(fd.Options)...).Value(&vals[i]))
		case core.FieldYesNo:
			yes[i] = fd.Default != "no"
			c := huh.NewConfirm().Title(fd.Label).Description(fd.Detail).Value(&yes[i])
			if fd.Yes != "" {
				c = c.Affirmative(fd.Yes)
			}
			if fd.No != "" {
				c = c.Negative(fd.No)
			}
			fields = append(fields, c)
		default:
			in := huh.NewInput().Title(fd.Label).Description(fd.Detail).Value(&vals[i])
			if fd.Kind == core.FieldSecret {
				in = in.EchoMode(huh.EchoModePassword)
			}
			if fd.Validate != nil {
				in = in.Validate(fd.Validate)
			}
			fields = append(fields, in)
		}
	}
	if err := runForm(ctx, huh.NewForm(huh.NewGroup(fields...))); err != nil {
		return out, err
	}
	for i, fd := range spec.Fields {
		switch fd.Kind {
		case core.FieldYesNo:
			out[fd.Key] = map[bool]string{true: "yes", false: "no"}[yes[i]]
		default:
			out[fd.Key] = vals[i]
		}
	}
	return out, nil
}

// File asks for a path in a form field (the terminal has no file chooser).
func (p termPrompter) File(ctx context.Context, r core.FileRequest) (string, error) {
	if !p.U.Interactive() {
		return "", nil
	}
	var v string
	in := huh.NewInput().Title(r.Title).Description(r.Detail).Value(&v)
	if r.Validate != nil {
		in = in.Validate(r.Validate)
	}
	if err := one(ctx, in); err != nil {
		return "", err
	}
	return strings.Trim(v, `"' `), nil
}

func (p termPrompter) Working(ctx context.Context, title string, f func(context.Context) error) error {
	return working(ctx, title, f)
}

func huhOptions(options []core.Option) []huh.Option[string] {
	out := make([]huh.Option[string], 0, len(options))
	for _, o := range options {
		out = append(out, huh.NewOption(o.Label, o.Value))
	}
	return out
}

// download runs one batch of inputs of any mix through the shared pipeline, asking its
// questions in the terminal.
func download(ctx context.Context, u *ui.UI, o opts, inputs ...string) error {
	_, err := core.Download(ctx, inputs, o.core(), core.Deps{UI: u, Prompter: newPrompter(u)})
	return err
}
