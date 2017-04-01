package commands

import (
	"fmt"
	"io"
	"os"

	core "github.com/ipfs/go-ipfs/core"
	coreunix "github.com/ipfs/go-ipfs/core/coreunix"
	cmds "gx/ipfs/QmQPnFKriy2hDHMdApoR859ENy2SmptX4wUJuvc1wir3Xt/go-ipfs-cmds"
	"gx/ipfs/QmYiqbfRCkryYvJsxBopy77YEhxNZXTmq5Y2qiKyenc59C/go-ipfs-cmdkit"

	context "context"
)

const progressBarMinSize = 1024 * 1024 * 8 // show progress bar for outputs > 8MiB

var CatCmd = &cmds.Command{
	Helptext: cmdsutil.HelpText{
		Tagline:          "Show IPFS object data.",
		ShortDescription: "Displays the data contained by an IPFS or IPNS object(s) at the given path.",
	},

	Arguments: []cmdsutil.Argument{
		cmdsutil.StringArg("ipfs-path", true, true, "The path to the IPFS object(s) to be outputted.").EnableStdin(),
	},
	Run: func(req cmds.Request, re cmds.ResponseEmitter) {
		log.Debugf("cat: RespEm type is %T", re)
		node, err := req.InvocContext().GetNode()
		if err != nil {
			err2 := re.SetError(err, cmdsutil.ErrNormal)
			if err2 != nil {
				log.Error(err)
			}
			return
		}

		if !node.OnlineMode() {
			if err := node.SetupOfflineRouting(); err != nil {
				err2 := re.SetError(err, cmdsutil.ErrNormal)
				if err2 != nil {
					log.Error(err)
				}
				return
			}
		}

		readers, length, err := cat(req.Context(), node, req.Arguments())
		log.Debug("cat returned ", readers, length, err)

		if err != nil {
			err2 := re.SetError(err, cmdsutil.ErrNormal)
			if err2 != nil {
				log.Error(err)
			}
			return
		}

		/*
			if err := corerepo.ConditionalGC(req.Context(), node, length); err != nil {
			err2 := re.SetError(err, cmdsutil.ErrNormal)
			if err2 != nil {
				log.Error(err)
			}

				return
			}
		*/

		re.SetLength(length)

		reader := io.MultiReader(readers...)
		// Since the reader returns the error that a block is missing, we need to take
		// Emit errors and send them to the client. Usually we don't do that because
		// it means the connection is broken or we supplied an illegal argument etc.
		err = re.Emit(reader)
		if err != nil {
			err = re.SetError(err, cmdsutil.ErrNormal)
			if err != nil {
				log.Error(err)
			}
		}
		re.Close()
	},
	PostRun: map[cmds.EncodingType]func(cmds.Request, cmds.ResponseEmitter) cmds.ResponseEmitter{
		cmds.CLI: func(req cmds.Request, re cmds.ResponseEmitter) cmds.ResponseEmitter {
			log.Debug("PostRun ohai")
			reNext, res := cmds.NewChanResponsePair(req)

			go func() {
				if res.Length() > 0 && res.Length() < progressBarMinSize {
					log.Debugf("cat.PR.go/res.Length() == %v < progressBarMinSize", res.Length())
					if err := cmds.Copy(re, res); err != nil {
						err2 := re.SetError(err, cmdsutil.ErrNormal)
						if err2 != nil {
							log.Error(err)
						}
						log.Debugf("cat.PR.go.if/cmd.Copy error: %s", err)
					}

					return
				}

				// Copy closes by itself, so we must not do this before
				defer re.Close()

				v, err := res.Next()
				log.Debugf("cat/res.Next() returned (%v, %v)", v, err)
				if err != nil {
					if err == cmds.ErrRcvdError {
						err2 := re.SetError(res.Error().Message, res.Error().Code)
						if err2 != nil {
							log.Error(err)
						}
					} else {
						err2 := re.SetError(res.Error(), cmdsutil.ErrNormal)
						if err2 != nil {
							log.Error(err)
						}
					}

					log.Debug("PostRun.go Next err=", err)
					return
				}

				r, ok := v.(io.Reader)
				if !ok {
					err2 := re.SetError(fmt.Sprintf("expected io.Reader, not %T", v), cmdsutil.ErrNormal)
					if err2 != nil {
						log.Error(err)
					}
					log.Debug("PostRun.go cast to io.Reader failed")
					return
				}

				bar, reader := progressBarForReader(os.Stderr, r, int64(res.Length()))
				bar.Start()

				log.Debug("PostRun.go.Emit()...")
				err = re.Emit(reader)
				if err != nil {
					log.Error(err)
				}
				log.Debug("PostRun.go.Emit()...done")
			}()

			return reNext
		},
	},
}

func cat(ctx context.Context, node *core.IpfsNode, paths []string) ([]io.Reader, uint64, error) {
	readers := make([]io.Reader, 0, len(paths))
	length := uint64(0)
	for _, fpath := range paths {
		log.Debug("cat.for: adding ", fpath)
		read, err := coreunix.Cat(ctx, node, fpath)
		if err != nil {
			return nil, 0, err
		}
		readers = append(readers, read)
		length += uint64(read.Size())
		log.Debug("cat.for: added reader of size", read.Size())
	}
	return readers, length, nil
}
