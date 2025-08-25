package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	_ "embed"
	"encoding/base64"
	"errors"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/crypto/openpgp"
)

const McoMustGatherSanitizerEncryptKeyEnvVar = "MCO_MUST_GATHER_SANITIZER_KEY"

//go:embed data/public-key.asc
var defaultGpgKey []byte

func archive(src string, target string) (err error) {
	entityList, err := getEncryptionKey()
	if err != nil {
		return err
	}
	buf := &bytes.Buffer{}
	if err := writeTar(src, buf); err != nil {
		return err
	}

	targetFile, err := os.OpenFile(target, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o666)
	if err != nil {
		return err
	}
	defer func() {
		if dstErr := targetFile.Close(); dstErr != nil {
			err = errors.Join(err, dstErr)
		}
	}()
	return encrypt(entityList, buf, targetFile)
}

func getEncryptionKey() (openpgp.EntityList, error) {
	// Early load and fail if the key is not present/valid
	content := os.Getenv(McoMustGatherSanitizerEncryptKeyEnvVar)
	var gpgBuffer *bytes.Buffer

	if content == "" {
		gpgBuffer = bytes.NewBuffer(defaultGpgKey)
	} else {
		rawBytes, err := base64.StdEncoding.DecodeString(content)
		if err != nil {
			return nil, err
		}
		gpgBuffer = bytes.NewBuffer(rawBytes)
	}

	entityList, err := openpgp.ReadArmoredKeyRing(gpgBuffer)
	if err != nil {
		return nil, err
	}
	return entityList, err
}

func writeTar(src string, writer io.Writer) (err error) {
	zr := gzip.NewWriter(writer)
	defer func() {
		if zrErr := zr.Close(); zrErr != nil {
			err = errors.Join(err, zrErr)
		}
	}()
	tw := tar.NewWriter(zr)
	defer func() {
		if twErr := tw.Close(); twErr != nil {
			err = errors.Join(err, twErr)
		}
	}()

	// walk through every file in the folder
	return filepath.Walk(src, func(file string, fi os.FileInfo, err error) error {
		relPath, err := filepath.Rel(src, file)
		if err != nil {
			return err
		}
		header, err := tar.FileInfoHeader(fi, relPath)
		if err != nil {
			return err
		}

		header.Name = filepath.ToSlash(relPath)

		if err := tw.WriteHeader(header); err != nil {
			return err
		}
		// if not a dir, write file content
		if !fi.IsDir() {
			data, err := os.Open(file)
			defer data.Close()
			if err != nil {
				return err
			}
			if _, err := io.Copy(tw, data); err != nil {
				return err
			}
		}
		return nil
	})
}

func encrypt(entities []*openpgp.Entity, reader io.Reader, writer io.Writer) (err error) {
	gpgWriter, err := openpgp.Encrypt(writer, entities, nil, &openpgp.FileHints{IsBinary: true}, nil)
	if err != nil {
		return err
	}
	defer func() {
		if wCloseErr := gpgWriter.Close(); wCloseErr != nil {
			err = errors.Join(err, wCloseErr)
		}
	}()
	if _, err := io.Copy(gpgWriter, reader); err != nil {
		return err
	}
	return err
}
