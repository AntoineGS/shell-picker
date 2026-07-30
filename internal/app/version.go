package app

const DefaultVersion = "dev"

func Version(build string) string {
	if build == "" {
		return DefaultVersion
	}
	return build
}
