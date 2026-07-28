package app

func Version(build string) string {
	if build == "" {
		return "dev"
	}
	return build
}
