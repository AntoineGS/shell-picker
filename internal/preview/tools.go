package preview

import "strings"

type directTool struct {
	name      string
	arguments []string
}

func richConverterArguments(category Category, path, artifact string) []string {
	switch category {
	case CategoryPDF:
		return []string{"-singlefile", "-jpeg", path, strings.TrimSuffix(artifact, ".jpg")}
	case CategoryVideo:
		return []string{"-i", path, "-o", artifact, "-s", "1080", "-m"}
	default:
		return []string{"-y", "-i", path, "-an", "-c:v", "copy", artifact}
	}
}
