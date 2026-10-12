package cli

import (
	"context"
	"io"
	"os"

	"github.com/CST-Cat/NodeDance/internal/control"
)

// Both terminal presentations use this executor, including partial-file cleanup
// and completion observation. Presentation code only binds input and renders.
func executeFinite(ctx context.Context, client *control.Client, catalog control.Catalog, o control.Operation, in control.Input, upload io.Reader, outputPath string, wait bool, observe func(control.Result)) control.Result {
	result := control.Result{Operation: o.Name}
	var download *os.File
	var writer io.Writer
	if o.Transport == "download" {
		if outputPath == "" {
			result.Error = "download requires an output file"
			return result
		}
		var err error
		download, err = os.OpenFile(outputPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			result.Error = err.Error()
			return result
		}
		writer = download
	} else if outputPath != "" {
		result.Error = "output file is supported only for downloads"
		return result
	}
	result = client.Execute(ctx, o, in, upload, writer)
	if download != nil {
		if result.Error == "" {
			if err := download.Sync(); err != nil {
				result.Error = err.Error()
			}
		}
		if err := download.Close(); err != nil {
			result.Error = err.Error()
		}
		if result.Error != "" {
			os.Remove(outputPath)
		} else {
			result.File = outputPath
		}
	}
	if wait && result.Error == "" {
		if observe != nil {
			observe(result)
		}
		result = client.Wait(ctx, catalog, o, in, result, observe)
	}
	return result
}

// The local registry is shared. Remote operations always come from Core.
func terminalOperations(catalog control.Catalog) []control.Operation {
	return append(append([]control.Operation{}, catalog.Operations...), localOperations()...)
}
