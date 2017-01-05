package loggerutils

import (
	"fmt"
	"io/ioutil"
	"os"
	"strconv"
	"sync"

	"github.com/docker/docker/pkg/archive"
	"github.com/docker/docker/pkg/pubsub"
)

// RotateFileWriter is Logger implementation for default Docker logging.
type RotateFileWriter struct {
	f            *os.File // store for closing
	mu           sync.Mutex
	capacity     int64  //maximum size of each file
	currentSize  int64  // current size of the latest file
	maxFiles     int    //maximum number of files
	compress     string // whether old versions of log files are compressed
	notifyRotate *pubsub.Publisher
}

//NewRotateFileWriter creates new RotateFileWriter
func NewRotateFileWriter(logPath string, capacity int64, maxFiles int, compress string) (*RotateFileWriter, error) {
	log, err := os.OpenFile(logPath, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0640)
	if err != nil {
		return nil, err
	}

	size, err := log.Seek(0, os.SEEK_END)
	if err != nil {
		return nil, err
	}

	return &RotateFileWriter{
		f:            log,
		capacity:     capacity,
		currentSize:  size,
		maxFiles:     maxFiles,
		compress:     compress,
		notifyRotate: pubsub.NewPublisher(0, 1),
	}, nil
}

//WriteLog write log message to File
func (w *RotateFileWriter) Write(message []byte) (int, error) {
	w.mu.Lock()
	if err := w.checkCapacityAndRotate(); err != nil {
		w.mu.Unlock()
		return -1, err
	}

	n, err := w.f.Write(message)
	if err == nil {
		w.currentSize += int64(n)
	}
	w.mu.Unlock()
	return n, err
}

func (w *RotateFileWriter) checkCapacityAndRotate() error {
	if w.capacity == -1 {
		return nil
	}

	if w.currentSize >= w.capacity {
		name := w.f.Name()
		if err := w.f.Close(); err != nil {
			return err
		}
		if err := rotate(name, w.maxFiles, w.compress); err != nil {
			return err
		}
		file, err := os.OpenFile(name, os.O_WRONLY|os.O_TRUNC|os.O_CREATE, 06400)
		if err != nil {
			return err
		}
		w.f = file
		w.currentSize = 0
		w.notifyRotate.Publish(struct{}{})
	}

	return nil
}

func rotate(name string, maxFiles int, compress string) error {
	if maxFiles < 2 {
		return nil
	}

	extension := ""
	var compressionAlg archive.Compression
	if compress != "" {
		switch compress {
		case "gzip":
			compressionAlg = archive.Gzip
			extension = ".gz"
		case "bzip2":
			compressionAlg = archive.Bzip2
			extension = ".bz"
		case "xz":
			compressionAlg = archive.Xz
			extension = ".xz"
		default:
			return fmt.Errorf("unknown compression algorithm %q for json-file", compress)
		}
	}

	for i := maxFiles - 1; i > 2; i-- {
		toPath := name + "." + strconv.Itoa(i) + extension
		fromPath := name + "." + strconv.Itoa(i-1) + extension
		if err := os.Rename(fromPath, toPath); err != nil && !os.IsNotExist(err) {
			return err
		}
	}

	if _, err := os.Stat(name + ".1"); err == nil && maxFiles > 2 {
		if err := os.Rename(name+".1", name+".2"); err != nil {
			return err
		}

		if err := compressFile(name+".2", compressionAlg, extension); err != nil {
			return err
		}
	}

	// The "[name].1" that jast renamed from "[name]" is not compressed
	// in order to prevent the log tracking tool from losing some historical
	// log data when a new log file is created.
	if err := os.Rename(name, name+".1"); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func compressFile(fileName string, compression archive.Compression, extension string) (err error) {
	outFile, err := os.OpenFile(fileName+extension, os.O_CREATE|os.O_RDWR, 0640)
	defer func() {
		outFile.Close()
		if err != nil {
			os.Remove(fileName + extension)
		}
	}()

	if err != nil {
		return err
	}

	compressWriter, err := archive.CompressStream(outFile, compression)
	defer compressWriter.Close()

	fileData, err := ioutil.ReadFile(fileName)
	if err != nil {
		return err
	}
	_, err = compressWriter.Write(fileData)
	if err != nil {
		return err
	}

	os.Remove(fileName)

	return nil
}

// LogPath returns the location the given writer logs to.
func (w *RotateFileWriter) LogPath() string {
	return w.f.Name()
}

// MaxFiles return maximum number of files
func (w *RotateFileWriter) MaxFiles() int {
	return w.maxFiles
}

//NotifyRotate returns the new subscriber
func (w *RotateFileWriter) NotifyRotate() chan interface{} {
	return w.notifyRotate.Subscribe()
}

//NotifyRotateEvict removes the specified subscriber from receiving any more messages.
func (w *RotateFileWriter) NotifyRotateEvict(sub chan interface{}) {
	w.notifyRotate.Evict(sub)
}

// Close closes underlying file and signals all readers to stop.
func (w *RotateFileWriter) Close() error {
	return w.f.Close()
}
