package releasepack

import (
	"archive/zip"
	"errors"
	"io"
	"io/fs"
	"time"
)

func writeZIP(output io.Writer, entries []archiveEntry, sourceTime time.Time, sources map[string]archiveSource, hooks archiveFileHooks) error {
	writer := zip.NewWriter(output)
	for _, entry := range entries {
		header := &zip.FileHeader{
			Name:     entry.Name,
			Comment:  "",
			Method:   zip.Deflate,
			Modified: sourceTime,
		}
		if entry.Directory {
			header.Method = zip.Store
			header.SetMode(fs.ModeDir | 0o755)
		} else {
			header.SetMode(entry.Mode.Perm())
		}

		entryWriter, err := writer.CreateHeader(header)
		if err != nil {
			_ = writer.Close()
			return err
		}
		if entry.Directory {
			continue
		}
		source, ok := sources[entry.SourcePath]
		if !ok {
			_ = writer.Close()
			return errors.New("missing archive source identity")
		}
		if err := copyArchiveSource(entryWriter, entry, source, hooks); err != nil {
			_ = writer.Close()
			return err
		}
	}
	return writer.Close()
}
