package candidate

import "github.com/AntoineGS/shell-picker/internal/pathutil"

type virtualRecordKey struct {
	kind   pathutil.Kind
	target string
	wire   string
}

func filesystemRecordKey(path []byte) string {
	return filesystemMergeKey(string(path))
}
