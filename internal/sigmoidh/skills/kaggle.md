---
name: kaggle
aliases: kaggle-terminal, kssh, colab, google-colab, gpu, notebook
description: Run code and transfer files on a free Kaggle or Google Colab GPU from the terminal — with the official Kaggle CLI, the official Colab CLI, or the kaggle-terminal relay for an interactive shell.
when: The user wants to run/train something on a Kaggle or Colab GPU, push or pull files to such a session, or mentions kaggle-terminal, kssh, the kaggle CLI, or the colab CLI.
requires: uv/pip (kaggle or google-colab-cli); the relay additionally needs cloudflared
---

# Kaggle / Colab remote GPU

Pick by platform and how much interaction you need:

| Need | Use | Shell access? | User step? |
|------|-----|----------------|------------|
| Kaggle, run a notebook/script headless | **official Kaggle CLI** (`uvx kaggle`) | **No** — batch kernels only | auth once |
| Colab, headless GPU/TPU, real SSH | **official Colab CLI** (`colab`) | **Yes** — `colab ssh` / `console` / `repl` | login once |
| Interactive shell on **Kaggle**, or file transfer | **kaggle-terminal relay** (`kssh.sh`) | Yes (real SSH) | start tunnel in the notebook |

SSH: the **Colab CLI** gives a real shell (`colab ssh`, or `colab console` for a raw TTY).
The **Kaggle CLI cannot** — it only pushes and runs kernels — so a Kaggle shell needs the
relay below.

## Kaggle — official CLI

`uvx kaggle` (or `pip install kaggle`) drives Kaggle without a browser. You push a
**kernel** (notebook/script), Kaggle runs it on its backend, then you pull the output.

```bash
uvx kaggle auth login                    # or ~/.kaggle/kaggle.json, or KAGGLE_USERNAME/KAGGLE_KEY
uvx kaggle quota                         # remaining weekly GPU/TPU quota

uvx kaggle kernels init -p myrun         # create myrun/kernel-metadata.json + a stub
# edit it: "id": "user/slug", "enable_gpu": true, "code_file": "run.ipynb",
#          "dataset_sources": [...], "competition_sources": [...], "kernel_sources": [...]
uvx kaggle kernels push -p myrun         # upload and run headless (no browser)
uvx kaggle kernels status user/slug
uvx kaggle kernels output -p myrun       # download the run's output files
uvx kaggle kernels logs user/slug
```

Also: `kaggle datasets …`, `kaggle models …`, `kaggle competitions …`,
`kaggle config …`. Credentials come from `kaggle auth login`, `~/.kaggle/kaggle.json`
(relocate with `KAGGLE_CONFIG_DIR`), or env vars. The repo ships agent skill files under
`skills/` (github.com/Kaggle/kaggle-cli).

## Colab — official CLI

Google's `colab` CLI provisions GPU/TPU runtimes with no notebook. Linux/macOS only.

```bash
uv tool install google-colab-cli         # or: pip install google-colab-cli
colab login                              # one-time, opens a browser
colab new --gpu T4                       # provision a session
colab install transformers datasets      # pip/uv onto the VM
colab exec -f train.py                   # run a script/notebook or stdin
colab upload local.csv /content/         # file transfer
colab download /content/model.safetensors .
colab ssh                                # real interactive shell (auto-creates a session if none)
colab console                            # raw TTY console
colab repl                               # Python REPL
colab log --output run.ipynb
colab stop
```

Plain `ssh`/`scp` against the runtime, via `~/.ssh/config`:

```
Host colab
    ProxyCommand colab ssh --proxy-mode -s SESS
    StrictHostKeyChecking no
```

`colab run [--gpu …] script.py` provisions a fresh VM, runs a script, and tears it
down in one shot; `colab repl`/`colab console` give interactivity; sessions stay alive
while the kernel is active. Omit `-s/--session` when only one is open.

## kaggle-terminal relay — interactive shell (both platforms)

Deploy github.com/adityabavadekar/kaggle-terminal to Vercel with `RELAY_SECRET` +
`DATABASE_URL`, then locally:

```bash
export RELAY_SECRET="secret"
curl -fsSL https://kagglessh.vercel.app/upload_key.sh | bash
curl -fsSL "https://kagglessh.vercel.app/kssh.sh?secret=$RELAY_SECRET" -o kssh.sh && chmod +x kssh.sh
```

Start the tunnel from a notebook cell, then drive it with `kssh.sh` (needs `cloudflared`):

```bash
./kssh.sh run "nvidia-smi"
./kssh.sh put train.py /kaggle/working/
./kssh.sh get /kaggle/working/out.csv .
```

| Platform | Setup script | Default remote dir | Free idle timeout |
|----------|--------------|--------------------|-------------------|
| Kaggle | `kaggle_setup.sh` | `/kaggle/working/` | 40 min |
| Google Colab | `colab_setup.sh` | `/content/` | 90 min |

**Kaggle needs the user here.** The relay tunnel is started by running a cell *inside the
notebook UI*, which you cannot do — ask the user to run it, then continue:

```
%env RELAY_SECRET=secret
!curl -fsSL https://kagglessh.vercel.app/kaggle_setup.sh | bash
```

## Long runs (do not get killed)

- Prefer the **headless** paths (Kaggle `kernels push`, `colab run`) — they need no open
  browser and survive your machine sleeping.
- Kaggle interactive dies after **40 min idle** (Colab free: 90 min); a local heartbeat
  `while true; do ./kssh.sh run "date" >/dev/null 2>&1; sleep 300; done &` keeps it alive.
- Overnight on Kaggle: relay `setup.sh -b` (blocks) + **Save & Run All (Commit)**, up to 12 h.
- Detach with tmux: `./kssh.sh run "tmux new-session -d -s train 'python train.py > train.log 2>&1'"`.
- Persist checkpoints to `/kaggle/working/` (exported on commit) or Colab Drive.

## Gotchas

- Free tiers have no guaranteed GPU (Kaggle weekly quota — check `kaggle quota`; Colab may give CPU).
- Kaggle kernel runs must set `enable_gpu: true` (or `enable_tpu`) in `kernel-metadata.json`.
- The Colab CLI supports Linux/macOS only; `colab login` needs a browser once.
- Relay: `RELAY_SECRET` is required on every `kssh.sh` call; `kssh.sh` rejects a malformed tunnel hostname.

## Verify

```bash
uvx kaggle quota                                          # Kaggle CLI reachable + quota
colab exec "import torch; print(torch.cuda.get_device_name(0))"   # Colab CLI
./kssh.sh run "nvidia-smi && pwd"                         # relay
```

Confirm the GPU and the working directory before starting a long job.
