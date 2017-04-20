package commands

import (
	"errors"
	"fmt"
	"io"
	"os"

	"gx/ipfs/QmQPnFKriy2hDHMdApoR859ENy2SmptX4wUJuvc1wir3Xt/go-ipfs-cmds"
	"gx/ipfs/QmYiqbfRCkryYvJsxBopy77YEhxNZXTmq5Y2qiKyenc59C/go-ipfs-cmdkit"
	"gx/ipfs/QmYiqbfRCkryYvJsxBopy77YEhxNZXTmq5Y2qiKyenc59C/go-ipfs-cmdkit/files"

	bstore "github.com/ipfs/go-ipfs/blocks/blockstore"
	blockservice "github.com/ipfs/go-ipfs/blockservice"
	core "github.com/ipfs/go-ipfs/core"
	"github.com/ipfs/go-ipfs/core/coreunix"
	offline "github.com/ipfs/go-ipfs/exchange/offline"
	dag "github.com/ipfs/go-ipfs/merkledag"
	dagtest "github.com/ipfs/go-ipfs/merkledag/test"
	mfs "github.com/ipfs/go-ipfs/mfs"
	ft "github.com/ipfs/go-ipfs/unixfs"

	"gx/ipfs/QmeWjRodbcZFKe5tMN7poEx3izym6osrLSnTLf9UjJZBbs/pb"
)

// ErrDepthLimitExceeded indicates that the max depth has been exceded.
var ErrDepthLimitExceeded = fmt.Errorf("depth limit exceeded")

const (
	quietOptionName       = "quiet"
	quieterOptionName     = "quieter"
	silentOptionName      = "silent"
	progressOptionName    = "progress"
	trickleOptionName     = "trickle"
	wrapOptionName        = "wrap-with-directory"
	hiddenOptionName      = "hidden"
	onlyHashOptionName    = "only-hash"
	chunkerOptionName     = "chunker"
	pinOptionName         = "pin"
	rawLeavesOptionName   = "raw-leaves"
	noCopyOptionName      = "nocopy"
	fstoreCacheOptionName = "fscache"
)

var AddCmd = &cmds.Command{
	Helptext: cmdsutil.HelpText{
		Tagline: "Add a file or directory to ipfs.",
		ShortDescription: `
Adds contents of <path> to ipfs. Use -r to add directories (recursively).
`,
		LongDescription: `
Adds contents of <path> to ipfs. Use -r to add directories.
Note that directories are added recursively, to form the ipfs
MerkleDAG.

The wrap option, '-w', wraps the file (or files, if using the
recursive option) in a directory. This directory contains only
the files which have been added, and means that the file retains
its filename. For example:

  > ipfs add example.jpg
  added QmbFMke1KXqnYyBBWxB74N4c5SBnJMVAiMNRcGu6x1AwQH example.jpg
  > ipfs add example.jpg -w
  added QmbFMke1KXqnYyBBWxB74N4c5SBnJMVAiMNRcGu6x1AwQH example.jpg
  added QmaG4FuMqEBnQNn3C8XJ5bpW8kLs7zq2ZXgHptJHbKDDVx

You can now refer to the added file in a gateway, like so:

  /ipfs/QmaG4FuMqEBnQNn3C8XJ5bpW8kLs7zq2ZXgHptJHbKDDVx/example.jpg
`,
	},

	Arguments: []cmdsutil.Argument{
		cmdsutil.FileArg("path", true, true, "The path to a file to be added to ipfs.").EnableRecursive().EnableStdin(),
	},
	Options: []cmdsutil.Option{
		cmdsutil.OptionRecursivePath, // a builtin option that allows recursive paths (-r, --recursive)
		cmdsutil.BoolOption(quietOptionName, "q", "Write minimal output."),
		cmdsutil.BoolOption(quieterOptionName, "Q", "Write only final hash."),
		cmdsutil.BoolOption(silentOptionName, "Write no output."),
		cmdsutil.BoolOption(progressOptionName, "p", "Stream progress data."),
		cmdsutil.BoolOption(trickleOptionName, "t", "Use trickle-dag format for dag generation."),
		cmdsutil.BoolOption(onlyHashOptionName, "n", "Only chunk and hash - do not write to disk."),
		cmdsutil.BoolOption(wrapOptionName, "w", "Wrap files with a directory object."),
		cmdsutil.BoolOption(hiddenOptionName, "H", "Include files that are hidden. Only takes effect on recursive add."),
		cmdsutil.StringOption(chunkerOptionName, "s", "Chunking algorithm to use."),
		cmdsutil.BoolOption(pinOptionName, "Pin this object when adding.").Default(true),
		cmdsutil.BoolOption(rawLeavesOptionName, "Use raw blocks for leaf nodes. (experimental)"),
		cmdsutil.BoolOption(noCopyOptionName, "Add the file using filestore. (experimental)"),
		cmdsutil.BoolOption(fstoreCacheOptionName, "Check the filestore for pre-existing blocks. (experimental)"),
	},
	PreRun: func(req cmds.Request) error {
		quiet, _, _ := req.Option(quietOptionName).Bool()
		quieter, _, _ := req.Option(quieterOptionName).Bool()
		quiet = quiet || quieter

		silent, _, _ := req.Option(silentOptionName).Bool()

		if quiet || silent {
			return nil
		}

		// ipfs cli progress bar defaults to true unless quiet or silent is used
		_, found, _ := req.Option(progressOptionName).Bool()
		if !found {
			req.SetOption(progressOptionName, true)
		}

		sizeFile, ok := req.Files().(files.SizeFile)
		if !ok {
			// we don't need to error, the progress bar just won't know how big the files are
			log.Warning("cannnot determine size of input file")
			return nil
		}

		sizeCh := make(chan int64, 1)
		req.Values()["size"] = sizeCh

		go func() {
			size, err := sizeFile.Size()
			if err != nil {
				log.Warningf("error getting files size: %s", err)
				// see comment above
				return
			}

			log.Debugf("Total size of file being added: %v\n", size)
			sizeCh <- size
		}()

		return nil
	},
	Run: func(req cmds.Request, re cmds.ResponseEmitter) {
		n, err := req.InvocContext().GetNode()
		if err != nil {
			err2 := re.SetError(err, cmdsutil.ErrNormal)
			if err2 != nil {
				log.Error(err)
			}
			return
		}

		cfg, err := n.Repo.Config()
		if err != nil {
			err2 := re.SetError(err, cmdsutil.ErrNormal)
			if err2 != nil {
				log.Error(err)
			}
			return
		}
		// check if repo will exceed storage limit if added
		// TODO: this doesn't handle the case if the hashed file is already in blocks (deduplicated)
		// TODO: conditional GC is disabled due to it is somehow not possible to pass the size to the daemon
		//if err := corerepo.ConditionalGC(req.Context(), n, uint64(size)); err != nil {
		//	res.SetError(err, cmdsutil.ErrNormal)
		//	return
		//}

		progress, _, _ := req.Option(progressOptionName).Bool()
		trickle, _, _ := req.Option(trickleOptionName).Bool()
		wrap, _, _ := req.Option(wrapOptionName).Bool()
		hash, _, _ := req.Option(onlyHashOptionName).Bool()
		hidden, _, _ := req.Option(hiddenOptionName).Bool()
		silent, _, _ := req.Option(silentOptionName).Bool()
		chunker, _, _ := req.Option(chunkerOptionName).String()
		dopin, _, _ := req.Option(pinOptionName).Bool()
		rawblks, rbset, _ := req.Option(rawLeavesOptionName).Bool()
		nocopy, _, _ := req.Option(noCopyOptionName).Bool()
		fscache, _, _ := req.Option(fstoreCacheOptionName).Bool()

		if nocopy && !cfg.Experimental.FilestoreEnabled {
			err2 := re.SetError(errors.New("filestore is not enabled, see https://git.io/vy4XN"),
				cmdsutil.ErrClient)
			if err2 != nil {
				log.Error(err)
			}

			return
		}

		if nocopy && !rbset {
			rawblks = true
		}

		if nocopy && !rawblks {
			err2 := re.SetError(fmt.Errorf("nocopy option requires '--raw-leaves' to be enabled as well"), cmdsutil.ErrNormal)
			if err2 != nil {
				log.Error(err)
			}

			return
		}

		if hash {
			nilnode, err := core.NewNode(n.Context(), &core.BuildCfg{
				//TODO: need this to be true or all files
				// hashed will be stored in memory!
				NilRepo: true,
			})
			if err != nil {
				err2 := re.SetError(err, cmdsutil.ErrNormal)
				if err2 != nil {
					log.Error(err)
				}
				return
			}
			n = nilnode
		}

		addblockstore := n.Blockstore
		if !(fscache || nocopy) {
			addblockstore = bstore.NewGCBlockstore(n.BaseBlocks, n.GCLocker)
		}

		exch := n.Exchange
		local, _, _ := req.Option("local").Bool()
		if local {
			exch = offline.Exchange(addblockstore)
		}

		bserv := blockservice.New(addblockstore, exch)
		dserv := dag.NewDAGService(bserv)

		outChan := make(chan interface{}, 8)

		fileAdder, err := coreunix.NewAdder(req.Context(), n.Pinning, n.Blockstore, dserv)
		if err != nil {
			err2 := re.SetError(err, cmdsutil.ErrNormal)
			if err2 != nil {
				log.Error(err)
			}
			return
		}

		fileAdder.Out = outChan
		fileAdder.Chunker = chunker
		fileAdder.Progress = progress
		fileAdder.Hidden = hidden
		fileAdder.Trickle = trickle
		fileAdder.Wrap = wrap
		fileAdder.Pin = dopin
		fileAdder.Silent = silent
		fileAdder.RawLeaves = rawblks
		fileAdder.NoCopy = nocopy

		if hash {
			md := dagtest.Mock()
			mr, err := mfs.NewRoot(req.Context(), md, ft.EmptyDirNode(), nil)
			if err != nil {
				err2 := re.SetError(err, cmdsutil.ErrNormal)
				if err2 != nil {
					log.Error(err)
				}
				return
			}

			fileAdder.SetMfsRoot(mr)
		}

		addAllAndPin := func(f files.File) error {
			// Iterate over each top-level file and add individually. Otherwise the
			// single files.File f is treated as a directory, affecting hidden file
			// semantics.
			for {
				file, err := f.NextFile()
				if err == io.EOF {
					// Finished the list of files.
					break
				} else if err != nil {
					return err
				}
				if err := fileAdder.AddFile(file); err != nil {
					return err
				}
			}

			// copy intermediary nodes from editor to our actual dagservice
			_, err := fileAdder.Finalize()
			if err != nil {
				return err
			}

			if hash {
				return nil
			}

			return fileAdder.PinRoot()
		}

		go func() {
			defer close(outChan)
			err := addAllAndPin(req.Files())
			if err != nil {
				err2 := re.SetError(err, cmdsutil.ErrNormal)
				if err2 != nil {
					log.Error(err)
				}
			}

			log.Debug("addAllAndPin returned ", err)
		}()

		defer re.Close()
		for v := range outChan {
			log.Debug("add.Run outChan rx ", v)
			err := re.Emit(v)
			if err != nil {
				log.Debugf("addCmd.Run emit error %s with value %#v", err, v)
				return
			}
		}
	},
	PostRun: map[cmds.EncodingType]func(cmds.Request, cmds.ResponseEmitter) cmds.ResponseEmitter{
		cmds.CLI: func(req cmds.Request, re cmds.ResponseEmitter) cmds.ResponseEmitter {
			reNext, res := cmds.NewChanResponsePair(req)
			outChan := make(chan interface{})

			progressBar := func(wait chan struct{}) {
				defer close(wait)

				quiet, _, _ := req.Option(quietOptionName).Bool()
				quieter, _, _ := req.Option(quieterOptionName).Bool()
				quiet = quiet || quieter

				progress, _, _ := req.Option(progressOptionName).Bool()

				var bar *pb.ProgressBar
				if progress {
					bar = pb.New64(0).SetUnits(pb.U_BYTES)
					bar.ManualUpdate = true
					bar.ShowTimeLeft = false
					bar.ShowPercent = false
					bar.Output = os.Stderr
					bar.Start()
				}

				var sizeChan chan int64
				s, found := req.Values()["size"]
				if found {
					sizeChan = s.(chan int64)
				}

				lastFile := ""
				lastHash := ""
				var totalProgress, prevFiles, lastBytes int64

			LOOP:
				for {
					select {
					case out, ok := <-outChan:
						if !ok {
							if quieter {
								fmt.Fprintln(os.Stdout, lastHash)
							}

							break LOOP
						}
						output := out.(*coreunix.AddedObject)
						if len(output.Hash) > 0 {
							lastHash = output.Hash
							if quieter {
								continue
							}

							if progress {
								// clear progress bar line before we print "added x" output
								fmt.Fprintf(os.Stderr, "\033[2K\r")
							}
							if quiet {
								fmt.Fprintf(os.Stdout, "%s\n", output.Hash)
							} else {
								fmt.Fprintf(os.Stdout, "added %s %s\n", output.Hash, output.Name)
							}

						} else {
							log.Debugf("add progress: %v %v\n", output.Name, output.Bytes)

							if !progress {
								continue
							}

							if len(lastFile) == 0 {
								lastFile = output.Name
							}
							if output.Name != lastFile || output.Bytes < lastBytes {
								prevFiles += lastBytes
								lastFile = output.Name
							}
							lastBytes = output.Bytes
							delta := prevFiles + lastBytes - totalProgress
							totalProgress = bar.Add64(delta)
						}

						if progress {
							bar.Update()
						}
					case size := <-sizeChan:
						if progress {
							bar.Total = size
							bar.ShowPercent = true
							bar.ShowBar = true
							bar.ShowTimeLeft = true
						}
					case <-req.Context().Done():
						err2 := re.SetError(req.Context().Err(), cmdsutil.ErrNormal)
						if err2 != nil {
							log.Error(req.Context().Err(), err2)
						}

						return
					}
				}
			}

			go func() {
				// defer order important! First close outChan, then wait for output to finish, then close re
				defer re.Close()

				if e := res.Error(); e != nil {
					err2 := re.SetError(e.Message, e.Code)
					if err2 != nil {
						log.Error(e, err2)
					}

					close(outChan)
					return
				}

				wait := make(chan struct{})
				go progressBar(wait)

				defer func() { <-wait }()
				defer close(outChan)

				for {
					v, err := res.Next()
					log.Debug("add PostRun: next returned ", v, err)

					if err != nil {
						// replace error by actual error - will be looked at by next if-statement
						if err == cmds.ErrRcvdError {
							err = res.Error()
						}

						if e, ok := err.(*cmdsutil.Error); ok {
							err2 := re.SetError(e.Message, e.Code)
							if err2 != nil {
								log.Error(err)
							}

						}

						if err != io.EOF {
							err2 := re.SetError(err, cmdsutil.ErrNormal)
							if err2 != nil {
								log.Error(err)
							}

						}

						return
					}

					outChan <- v
				}
			}()

			return reNext
		},
	},
	Type: coreunix.AddedObject{},
}
