package filesystem

import (
	"bytes"
	"errors"
	. "github.com/franela/goblin"
	"github.com/avatag-host/claws/config"
	"io/ioutil"
	"math/rand"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"unicode/utf8"
)

func NewFs() (*Filesystem, *rootFs) {
	config.Set(&config.Configuration{
		AuthenticationToken: "abc",
		System: config.SystemConfiguration{
			RootDirectory:     "/server",
			DiskCheckInterval: 150,
		},
	})

	tmpDir, err := ioutil.TempDir(os.TempDir(), "panther")
	if err != nil {
		panic(err)
	}
	// defer os.RemoveAll(tmpDir)

	rfs := rootFs{root: tmpDir}

	rfs.reset()

	fs := New(filepath.Join(tmpDir, "/server"), 0)
	fs.isTest = true

	return fs, &rfs
}

type rootFs struct {
	root string
}

func (rfs *rootFs) CreateServerFile(p string, c string) error {
	f, err := os.Create(filepath.Join(rfs.root, "/server", p))

	if err == nil {
		f.Write([]byte(c))
		f.Close()
	}

	return err
}

func (rfs *rootFs) StatServerFile(p string) (os.FileInfo, error) {
	return os.Stat(filepath.Join(rfs.root, "/server", p))
}

func (rfs *rootFs) reset() {
	if err := os.RemoveAll(filepath.Join(rfs.root, "/server")); err != nil {
		if !os.IsNotExist(err) {
			panic(err)
		}
	}

	if err := os.Mkdir(filepath.Join(rfs.root, "/server"), 0755); err != nil {
		panic(err)
	}
}

func TestFilesystem_Path(t *testing.T) {
	g := Goblin(t)
	fs, rfs := NewFs()

	g.Describe("Path", func() {
		g.It("returns the root path for the instance", func() {
			g.Assert(fs.Path()).Equal(filepath.Join(rfs.root, "/server"))
		})
	})
}

func TestFilesystem_SafePath(t *testing.T) {
	g := Goblin(t)
	fs, rfs := NewFs()
	prefix := filepath.Join(rfs.root, "/server")

	g.Describe("SafePath", func() {
		g.It("returns a cleaned path to a given file", func() {
			p, err := fs.SafePath("test.txt")
			g.Assert(err).IsNil()
			g.Assert(p).Equal(prefix + "/test.txt")

			p, err = fs.SafePath("/test.txt")
			g.Assert(err).IsNil()
			g.Assert(p).Equal(prefix + "/test.txt")

			p, err = fs.SafePath("./test.txt")
			g.Assert(err).IsNil()
			g.Assert(p).Equal(prefix + "/test.txt")

			p, err = fs.SafePath("/foo/../test.txt")
			g.Assert(err).IsNil()
			g.Assert(p).Equal(prefix + "/test.txt")

			p, err = fs.SafePath("/foo/bar")
			g.Assert(err).IsNil()
			g.Assert(p).Equal(prefix + "/foo/bar")
		})

		g.It("handles root directory access", func() {
			p, err := fs.SafePath("/")
			g.Assert(err).IsNil()
			g.Assert(p).Equal(prefix)

			p, err = fs.SafePath("")
			g.Assert(err).IsNil()
			g.Assert(p).Equal(prefix)
		})

		g.It("removes trailing slashes from paths", func() {
			p, err := fs.SafePath("/foo/bar/")
			g.Assert(err).IsNil()
			g.Assert(p).Equal(prefix + "/foo/bar")
		})

		g.It("handles deeply nested directories that do not exist", func() {
			p, err := fs.SafePath("/foo/bar/baz/quaz/../../ducks/testing.txt")
			g.Assert(err).IsNil()
			g.Assert(p).Equal(prefix + "/foo/bar/ducks/testing.txt")
		})

		g.It("blocks access to files outside the root directory", func() {
			p, err := fs.SafePath("../test.txt")
			g.Assert(err).IsNotNil()
			g.Assert(errors.Is(err, ErrBadPathResolution)).IsTrue()
			g.Assert(p).Equal("")

			p, err = fs.SafePath("/../test.txt")
			g.Assert(err).IsNotNil()
			g.Assert(errors.Is(err, ErrBadPathResolution)).IsTrue()
			g.Assert(p).Equal("")

			p, err = fs.SafePath("./foo/../../test.txt")
			g.Assert(err).IsNotNil()
			g.Assert(errors.Is(err, ErrBadPathResolution)).IsTrue()
			g.Assert(p).Equal("")

			p, err = fs.SafePath("..")
			g.Assert(err).IsNotNil()
			g.Assert(errors.Is(err, ErrBadPathResolution)).IsTrue()
			g.Assert(p).Equal("")
		})
	})
}

// We test against accessing files outside the root directory in the tests, however it
// is still possible for someone to mess up and not properly use this safe path call. In
// order to truly confirm this, we'll try to pass in a symlinked malicious file to all of
// the calls and ensure they all fail with the same reason.
func TestFilesystem_Blocks_Symlinks(t *testing.T) {
	g := Goblin(t)
	fs, rfs := NewFs()

	if err := rfs.CreateServerFile("/../malicious.txt", "external content"); err != nil {
		panic(err)
	}

	if err := os.Mkdir(filepath.Join(rfs.root, "/malicious_dir"), 0777); err != nil {
		panic(err)
	}

	if err := os.Symlink(filepath.Join(rfs.root, "malicious.txt"), filepath.Join(rfs.root, "/server/symlinked.txt")); err != nil {
		panic(err)
	}

	if err := os.Symlink(filepath.Join(rfs.root, "/malicious_dir"), filepath.Join(rfs.root, "/server/external_dir")); err != nil {
		panic(err)
	}

	g.Describe("Readfile", func() {
		g.It("cannot read a file symlinked outside the root", func() {
			b := bytes.Buffer{}

			err := fs.Readfile("symlinked.txt", &b)
			g.Assert(err).IsNotNil()
			g.Assert(errors.Is(err, ErrBadPathResolution)).IsTrue()
		})
	})

	g.Describe("Writefile", func() {
		g.It("cannot write to a file symlinked outside the root", func() {
			r := bytes.NewReader([]byte("testing"))

			err := fs.Writefile("symlinked.txt", r)
			g.Assert(err).IsNotNil()
			g.Assert(errors.Is(err, ErrBadPathResolution)).IsTrue()
		})

		g.It("cannot write a file to a directory symlinked outside the root", func() {
			r := bytes.NewReader([]byte("testing"))

			err := fs.Writefile("external_dir/foo.txt", r)
			g.Assert(err).IsNotNil()
			g.Assert(errors.Is(err, ErrBadPathResolution)).IsTrue()
		})
	})

	g.Describe("CreateDirectory", func() {
		g.It("cannot create a directory outside the root", func() {
			err := fs.CreateDirectory("my_dir", "external_dir")
			g.Assert(err).IsNotNil()
			g.Assert(errors.Is(err, ErrBadPathResolution)).IsTrue()
		})

		g.It("cannot create a nested directory outside the root", func() {
			err := fs.CreateDirectory("my/nested/dir", "external_dir/foo/bar")
			g.Assert(err).IsNotNil()
			g.Assert(errors.Is(err, ErrBadPathResolution)).IsTrue()
		})

		g.It("cannot create a nested directory outside the root", func() {
			err := fs.CreateDirectory("my/nested/dir", "external_dir/server")
			g.Assert(err).IsNotNil()
			g.Assert(errors.Is(err, ErrBadPathResolution)).IsTrue()
		})
	})

	g.Describe("Rename", func() {
		g.It("cannot rename a file symlinked outside the directory root", func() {
			err := fs.Rename("symlinked.txt", "foo.txt")
			g.Assert(err).IsNotNil()
			g.Assert(errors.Is(err, ErrBadPathResolution)).IsTrue()
		})

		g.It("cannot rename a symlinked directory outside the root", func() {
			err := fs.Rename("external_dir", "foo")
			g.Assert(err).IsNotNil()
			g.Assert(errors.Is(err, ErrBadPathResolution)).IsTrue()
		})

		g.It("cannot rename a file to a location outside the directory root", func() {
			rfs.CreateServerFile("my_file.txt", "internal content")

			err := fs.Rename("my_file.txt", "external_dir/my_file.txt")
			g.Assert(err).IsNotNil()
			g.Assert(errors.Is(err, ErrBadPathResolution)).IsTrue()
		})
	})

	g.Describe("Chown", func() {
		g.It("cannot chown a file symlinked outside the directory root", func() {
			err := fs.Chown("symlinked.txt")
			g.Assert(err).IsNotNil()
			g.Assert(errors.Is(err, ErrBadPathResolution)).IsTrue()
		})

		g.It("cannot chown a directory symlinked outside the directory root", func() {
			err := fs.Chown("external_dir")
			g.Assert(err).IsNotNil()
			g.Assert(errors.Is(err, ErrBadPathResolution)).IsTrue()
		})
	})

	g.Describe("Copy", func() {
		g.It("cannot copy a file symlinked outside the directory root", func() {
			err := fs.Copy("symlinked.txt")
			g.Assert(err).IsNotNil()
			g.Assert(errors.Is(err, ErrBadPathResolution)).IsTrue()
		})
	})

	g.Describe("Delete", func() {
		g.It("deletes the symlinked file but leaves the source", func() {
			err := fs.Delete("symlinked.txt")
			g.Assert(err).IsNil()

			_, err = os.Stat(filepath.Join(rfs.root, "malicious.txt"))
			g.Assert(err).IsNil()

			_, err = rfs.StatServerFile("symlinked.txt")
			g.Assert(err).IsNotNil()
			g.Assert(errors.Is(err, os.ErrNotExist)).IsTrue()
		})
	})

	rfs.reset()
}

func TestFilesystem_Readfile(t *testing.T) {
	g := Goblin(t)
	fs, rfs := NewFs()

	g.Describe("Readfile", func() {
		buf := &bytes.Buffer{}

		g.It("opens a file if it exists on the system", func() {
			err := rfs.CreateServerFile("test.txt", "testing")
			g.Assert(err).IsNil()

			err = fs.Readfile("test.txt", buf)
			g.Assert(err).IsNil()
			g.Assert(buf.String()).Equal("testing")
		})

		g.It("returns an error if the file does not exist", func() {
			err := fs.Readfile("test.txt", buf)
			g.Assert(err).IsNotNil()
			g.Assert(errors.Is(err, os.ErrNotExist)).IsTrue()
		})

		g.It("returns an error if the \"file\" is a directory", func() {
			err := os.Mkdir(filepath.Join(rfs.root, "/server/test.txt"), 0755)
			g.Assert(err).IsNil()

			err = fs.Readfile("test.txt", buf)
			g.Assert(err).IsNotNil()
			g.Assert(errors.Is(err, ErrIsDirectory)).IsTrue()
		})

		g.It("cannot open a file outside the root directory", func() {
			err := rfs.CreateServerFile("/../test.txt", "testing")
			g.Assert(err).IsNil()

			err = fs.Readfile("/../test.txt", buf)
			g.Assert(err).IsNotNil()
			g.Assert(errors.Is(err, ErrBadPathResolution)).IsTrue()
		})

		g.AfterEach(func() {
			buf.Truncate(0)
			atomic.StoreInt64(&fs.diskUsed, 0)
			rfs.reset()
		})
	})
}

func TestFilesystem_Writefile(t *testing.T) {
	g := Goblin(t)
	fs, rfs := NewFs()

	g.Describe("Open and WriteFile", func() {
		buf := &bytes.Buffer{}

		// Test that a file can be written to the disk and that the disk space used as a result
		// is updated correctly in the end.
		g.It("can create a new file", func() {
			r := bytes.NewReader([]byte("test file content"))

			g.Assert(atomic.LoadInt64(&fs.diskUsed)).Equal(int64(0))

			err := fs.Writefile("test.txt", r)
			g.Assert(err).IsNil()

			err = fs.Readfile("test.txt", buf)
			g.Assert(err).IsNil()
			g.Assert(buf.String()).Equal("test file content")
			g.Assert(atomic.LoadInt64(&fs.diskUsed)).Equal(r.Size())
		})

		g.It("can create a new file inside a nested directory with leading slash", func() {
			r := bytes.NewReader([]byte("test file content"))

			err := fs.Writefile("/some/nested/test.txt", r)
			g.Assert(err).IsNil()

			err = fs.Readfile("/some/nested/test.txt", buf)
			g.Assert(err).IsNil()
			g.Assert(buf.String()).Equal("test file content")
		})

		g.It("can create a new file inside a nested directory without a trailing slash", func() {
			r := bytes.NewReader([]byte("test file content"))

			err := fs.Writefile("some/../foo/bar/test.txt", r)
			g.Assert(err).IsNil()

			err = fs.Readfile("foo/bar/test.txt", buf)
			g.Assert(err).IsNil()
			g.Assert(buf.String()).Equal("test file content")
		})

		g.It("cannot create a file outside the root directory", func() {
			r := bytes.NewReader([]byte("test file content"))

			err := fs.Writefile("/some/../foo/../../test.txt", r)
			g.Assert(err).IsNotNil()
			g.Assert(errors.Is(err, ErrBadPathResolution)).IsTrue()
		})

		g.It("cannot write a file that exceeds the disk limits", func() {
			atomic.StoreInt64(&fs.diskLimit, 1024)

			b := make([]byte, 1025)
			_, err := rand.Read(b)
			g.Assert(err).IsNil()
			g.Assert(len(b)).Equal(1025)

			r := bytes.NewReader(b)
			err = fs.Writefile("test.txt", r)
			g.Assert(err).IsNotNil()
			g.Assert(errors.Is(err, ErrNotEnoughDiskSpace)).IsTrue()
		})

		/*g.It("updates the total space used when a file is appended to", func() {
			atomic.StoreInt64(&fs.diskUsed, 100)

			b := make([]byte, 100)
			_, _ = rand.Read(b)

			r := bytes.NewReader(b)
			err := fs.Writefile("test.txt", r)
			g.Assert(err).IsNil()
			g.Assert(atomic.LoadInt64(&fs.diskUsed)).Equal(int64(200))

			// If we write less data than already exists, we should expect the total
			// disk used to be decremented.
			b = make([]byte, 50)
			_, _ = rand.Read(b)

			r = bytes.NewReader(b)
			err = fs.Writefile("test.txt", r)
			g.Assert(err).IsNil()
			g.Assert(atomic.LoadInt64(&fs.diskUsed)).Equal(int64(150))
		})*/

		g.It("truncates the file when writing new contents", func() {
			r := bytes.NewReader([]byte("original data"))
			err := fs.Writefile("test.txt", r)
			g.Assert(err).IsNil()

			r = bytes.NewReader([]byte("new data"))
			err = fs.Writefile("test.txt", r)
			g.Assert(err).IsNil()

			err = fs.Readfile("test.txt", buf)
			g.Assert(err).IsNil()
			g.Assert(buf.String()).Equal("new data")
		})

		g.AfterEach(func() {
			buf.Truncate(0)
			rfs.reset()

			atomic.StoreInt64(&fs.diskUsed, 0)
			atomic.StoreInt64(&fs.diskLimit, 0)
		})
	})
}

func TestFilesystem_CreateDirectory(t *testing.T) {
	g := Goblin(t)
	fs, rfs := NewFs()

	g.Describe("CreateDirectory", func() {
		g.It("should create missing directories automatically", func() {
			err := fs.CreateDirectory("test", "foo/bar/baz")
			g.Assert(err).IsNil()

			st, err := rfs.StatServerFile("foo/bar/baz/test")
			g.Assert(err).IsNil()
			g.Assert(st.IsDir()).IsTrue()
			g.Assert(st.Name()).Equal("test")
		})

		g.It("should work with leading and trailing slashes", func() {
			err := fs.CreateDirectory("test", "/foozie/barzie/bazzy/")
			g.Assert(err).IsNil()

			st, err := rfs.StatServerFile("foozie/barzie/bazzy/test")
			g.Assert(err).IsNil()
			g.Assert(st.IsDir()).IsTrue()
			g.Assert(st.Name()).Equal("test")
		})

		g.It("should not allow the creation of directories outside the root", func() {
			err := fs.CreateDirectory("test", "e/../../something")
			g.Assert(err).IsNotNil()
			g.Assert(errors.Is(err, ErrBadPathResolution)).IsTrue()
		})

		g.It("should not increment the disk usage", func() {
			err := fs.CreateDirectory("test", "/")
			g.Assert(err).IsNil()
			g.Assert(atomic.LoadInt64(&fs.diskUsed)).Equal(int64(0))
		})

		g.AfterEach(func() {
			rfs.reset()
		})
	})
}

func TestFilesystem_Rename(t *testing.T) {
	g := Goblin(t)
	fs, rfs := NewFs()

	g.Describe("Rename", func() {
		g.BeforeEach(func() {
			if err := rfs.CreateServerFile("source.txt", "text content"); err != nil {
				panic(err)
			}
		})

		g.It("returns an error if the target already exists", func() {
			err := rfs.CreateServerFile("target.txt", "taget content")
			g.Assert(err).IsNil()

			err = fs.Rename("source.txt", "target.txt")
			g.Assert(err).IsNotNil()
			g.Assert(errors.Is(err, os.ErrExist)).IsTrue()
		})

		g.It("returns an error if the final destination is the root directory", func() {
			err := fs.Rename("source.txt", "/")
			g.Assert(err).IsNotNil()
			g.Assert(errors.Is(err, os.ErrExist)).IsTrue()
		})

		g.It("returns an error if the source destination is the root directory", func() {
			err := fs.Rename("source.txt", "/")
			g.Assert(err).IsNotNil()
			g.Assert(errors.Is(err, os.ErrExist)).IsTrue()
		})

		g.It("does not allow renaming to a location outside the root", func() {
			err := fs.Rename("source.txt", "../target.txt")
			g.Assert(err).IsNotNil()
			g.Assert(errors.Is(err, ErrBadPathResolution)).IsTrue()
		})

		g.It("does not allow renaming from a location outside the root", func() {
			err := rfs.CreateServerFile("/../ext-source.txt", "taget content")

			err = fs.Rename("/../ext-source.txt", "target.txt")
			g.Assert(err).IsNotNil()
			g.Assert(errors.Is(err, ErrBadPathResolution)).IsTrue()
		})

		g.It("allows a file to be renamed", func() {
			err := fs.Rename("source.txt", "target.txt")
			g.Assert(err).IsNil()

			_, err = rfs.StatServerFile("source.txt")
			g.Assert(err).IsNotNil()
			g.Assert(errors.Is(err, os.ErrNotExist)).IsTrue()

			st, err := rfs.StatServerFile("target.txt")
			g.Assert(err).IsNil()
			g.Assert(st.Name()).Equal("target.txt")
			g.Assert(st.Size()).IsNotZero()
		})

		g.It("allows a folder to be renamed", func() {
			err := os.Mkdir(filepath.Join(rfs.root, "/server/source_dir"), 0755)
			g.Assert(err).IsNil()

			err = fs.Rename("source_dir", "target_dir")
			g.Assert(err).IsNil()

			_, err = rfs.StatServerFile("source_dir")
			g.Assert(err).IsNotNil()
			g.Assert(errors.Is(err, os.ErrNotExist)).IsTrue()

			st, err := rfs.StatServerFile("target_dir")
			g.Assert(err).IsNil()
			g.Assert(st.IsDir()).IsTrue()
		})

		g.It("returns an error if the source does not exist", func() {
			err := fs.Rename("missing.txt", "target.txt")
			g.Assert(err).IsNotNil()
			g.Assert(errors.Is(err, os.ErrNotExist)).IsTrue()
		})

		g.It("creates directories if they are missing", func() {
			err := fs.Rename("source.txt", "nested/folder/target.txt")
			g.Assert(err).IsNil()

			st, err := rfs.StatServerFile("nested/folder/target.txt")
			g.Assert(err).IsNil()
			g.Assert(st.Name()).Equal("target.txt")
		})

		g.AfterEach(func() {
			rfs.reset()
		})
	})
}

func TestFilesystem_Copy(t *testing.T) {
	g := Goblin(t)
	fs, rfs := NewFs()

	g.Describe("Copy", func() {
		g.BeforeEach(func() {
			if err := rfs.CreateServerFile("source.txt", "text content"); err != nil {
				panic(err)
			}

			atomic.StoreInt64(&fs.diskUsed, int64(utf8.RuneCountInString("test content")))
		})

		g.It("should return an error if the source does not exist", func() {
			err := fs.Copy("foo.txt")
			g.Assert(err).IsNotNil()
			g.Assert(errors.Is(err, os.ErrNotExist)).IsTrue()
		})

		g.It("should return an error if the source is outside the root", func() {
			err := rfs.CreateServerFile("/../ext-source.txt", "text content")

			err = fs.Copy("../ext-source.txt")
			g.Assert(err).IsNotNil()
			g.Assert(errors.Is(err, ErrBadPathResolution)).IsTrue()
		})

		g.It("should return an error if the source directory is outside the root", func() {
			err := os.MkdirAll(filepath.Join(rfs.root, "/nested/in/dir"), 0755)
			g.Assert(err).IsNil()

			err = rfs.CreateServerFile("/../nested/in/dir/ext-source.txt", "external content")
			g.Assert(err).IsNil()

			err = fs.Copy("../nested/in/dir/ext-source.txt")
			g.Assert(err).IsNotNil()
			g.Assert(errors.Is(err, ErrBadPathResolution)).IsTrue()

			err = fs.Copy("nested/in/../../../nested/in/dir/ext-source.txt")
			g.Assert(err).IsNotNil()
			g.Assert(errors.Is(err, ErrBadPathResolution)).IsTrue()
		})

		g.It("should return an error if the source is a directory", func() {
			err := os.Mkdir(filepath.Join(rfs.root, "/server/dir"), 0755)
			g.Assert(err).IsNil()

			err = fs.Copy("dir")
			g.Assert(err).IsNotNil()
			g.Assert(errors.Is(err, os.ErrNotExist)).IsTrue()
		})

		g.It("should return an error if there is not space to copy the file", func() {
			atomic.StoreInt64(&fs.diskLimit, 2)

			err := fs.Copy("source.txt")
			g.Assert(err).IsNotNil()
			g.Assert(errors.Is(err, ErrNotEnoughDiskSpace)).IsTrue()
		})

		g.It("should create a copy of the file and increment the disk used", func() {
			err := fs.Copy("source.txt")
			g.Assert(err).IsNil()

			_, err = rfs.StatServerFile("source.txt")
			g.Assert(err).IsNil()

			_, err = rfs.StatServerFile("source copy.txt")
			g.Assert(err).IsNil()
		})

		g.It("should create a copy of the file with a suffix if a copy already exists", func() {
			err := fs.Copy("source.txt")
			g.Assert(err).IsNil()

			err = fs.Copy("source.txt")
			g.Assert(err).IsNil()

			r := []string{"source.txt", "source copy.txt", "source copy 1.txt"}

			for _, name := range r {
				_, err = rfs.StatServerFile(name)
				g.Assert(err).IsNil()
			}

			g.Assert(atomic.LoadInt64(&fs.diskUsed)).Equal(int64(utf8.RuneCountInString("test content")) * 3)
		})

		g.It("should create a copy inside of a directory", func() {
			err := os.MkdirAll(filepath.Join(rfs.root, "/server/nested/in/dir"), 0755)
			g.Assert(err).IsNil()

			err = rfs.CreateServerFile("nested/in/dir/source.txt", "test content")
			g.Assert(err).IsNil()

			err = fs.Copy("nested/in/dir/source.txt")
			g.Assert(err).IsNil()

			_, err = rfs.StatServerFile("nested/in/dir/source.txt")
			g.Assert(err).IsNil()

			_, err = rfs.StatServerFile("nested/in/dir/source copy.txt")
			g.Assert(err).IsNil()
		})

		g.AfterEach(func() {
			rfs.reset()

			atomic.StoreInt64(&fs.diskUsed, 0)
			atomic.StoreInt64(&fs.diskLimit, 0)
		})
	})
}

func TestFilesystem_Delete(t *testing.T) {
	g := Goblin(t)
	fs, rfs := NewFs()

	g.Describe("Delete", func() {
		g.BeforeEach(func() {
			if err := rfs.CreateServerFile("source.txt", "test content"); err != nil {
				panic(err)
			}

			atomic.StoreInt64(&fs.diskUsed, int64(utf8.RuneCountInString("test content")))
		})

		g.It("does not delete files outside the root directory", func() {
			err := rfs.CreateServerFile("/../ext-source.txt", "external content")

			err = fs.Delete("../ext-source.txt")
			g.Assert(err).IsNotNil()
			g.Assert(errors.Is(err, ErrBadPathResolution)).IsTrue()
		})

		g.It("does not allow the deletion of the root directory", func() {
			err := fs.Delete("/")
			g.Assert(err).IsNotNil()
			g.Assert(err.Error()).Equal("cannot delete root server directory")
		})

		g.It("does not return an error if the target does not exist", func() {
			err := fs.Delete("missing.txt")
			g.Assert(err).IsNil()

			st, err := rfs.StatServerFile("source.txt")
			g.Assert(err).IsNil()
			g.Assert(st.Name()).Equal("source.txt")
		})

		g.It("deletes files and subtracts their size from the disk usage", func() {
			err := fs.Delete("source.txt")
			g.Assert(err).IsNil()

			_, err = rfs.StatServerFile("source.txt")
			g.Assert(err).IsNotNil()
			g.Assert(errors.Is(err, os.ErrNotExist)).IsTrue()

			g.Assert(atomic.LoadInt64(&fs.diskUsed)).Equal(int64(0))
		})

		g.It("deletes all items inside a directory if the directory is deleted", func() {
			sources := []string{
				"foo/source.txt",
				"foo/bar/source.txt",
				"foo/bar/baz/source.txt",
			}

			err := os.MkdirAll(filepath.Join(rfs.root, "/server/foo/bar/baz"), 0755)
			g.Assert(err).IsNil()

			for _, s := range sources {
				err = rfs.CreateServerFile(s, "test content")
				g.Assert(err).IsNil()
			}

			atomic.StoreInt64(&fs.diskUsed, int64(utf8.RuneCountInString("test content")*3))

			err = fs.Delete("foo")
			g.Assert(err).IsNil()
			g.Assert(atomic.LoadInt64(&fs.diskUsed)).Equal(int64(0))

			for _, s := range sources {
				_, err = rfs.StatServerFile(s)
				g.Assert(err).IsNotNil()
				g.Assert(errors.Is(err, os.ErrNotExist)).IsTrue()
			}
		})

		g.AfterEach(func() {
			rfs.reset()

			atomic.StoreInt64(&fs.diskUsed, 0)
			atomic.StoreInt64(&fs.diskLimit, 0)
		})
	})
}
