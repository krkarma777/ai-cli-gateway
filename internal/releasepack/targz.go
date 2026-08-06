package releasepack

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"io"
	"time"
)

func writeTarGzip(output io.Writer, entries []archiveEntry, sourceTime time.Time, sources map[string]archiveSource, hooks archiveFileHooks) error {
	gzipWriter := gzip.NewWriter(output)
	gzipWriter.Name = ""
	gzipWriter.Comment = ""
	gzipWriter.Extra = nil
	gzipWriter.ModTime = sourceTime
	gzipWriter.OS = 255

	tarWriter := tar.NewWriter(gzipWriter)
	for _, entry := range entries {
		header := &tar.Header{
			Name:       entry.Name,
			Mode:       int64(entry.Mode.Perm()),
			Uid:        0,
			Gid:        0,
			Size:       0,
			ModTime:    sourceTime,
			Typeflag:   tar.TypeDir,
			Linkname:   "",
			Uname:      "",
			Gname:      "",
			Format:     tar.FormatUSTAR,
			AccessTime: time.Time{},
			ChangeTime: time.Time{},
		}
		if !entry.Directory {
			source, ok := sources[entry.SourcePath]
			if !ok {
				closeTarWriters(tarWriter, gzipWriter)
				return errors.New("missing archive source identity")
			}
			header.Typeflag = tar.TypeReg
			header.Size = source.Size
		}
		if err := tarWriter.WriteHeader(header); err != nil {
			closeTarWriters(tarWriter, gzipWriter)
			return err
		}
		if !entry.Directory {
			if err := copyArchiveSource(tarWriter, entry, sources[entry.SourcePath], hooks); err != nil {
				closeTarWriters(tarWriter, gzipWriter)
				return err
			}
		}
	}
	if err := tarWriter.Close(); err != nil {
		_ = gzipWriter.Close()
		return err
	}
	return gzipWriter.Close()
}

func closeTarWriters(tarWriter *tar.Writer, gzipWriter *gzip.Writer) {
	_ = tarWriter.Close()
	_ = gzipWriter.Close()
}
