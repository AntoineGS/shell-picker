package process

import "context"

func (runner Runner) Run(ctx context.Context, spec Spec) error {
	if runner.Execute != nil {
		if err := validateSpec(ctx, spec); err != nil {
			return err
		}
		return runner.Execute(ctx, spec)
	}
	child, err := runner.Start(ctx, spec)
	if err != nil {
		return err
	}
	return child.Wait()
}
