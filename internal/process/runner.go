package process

import "context"

func (runner Runner) Run(ctx context.Context, spec Spec) error {
	if err := validateSpec(ctx, spec); err != nil {
		return err
	}
	if runner.BeforeStart != nil {
		if err := runner.BeforeStart(spec); err != nil {
			return err
		}
	}
	child, err := runner.Start(ctx, spec)
	if err != nil {
		return err
	}
	return child.Wait()
}
