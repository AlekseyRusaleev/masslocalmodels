# Linux node

The window exists only on Windows. On Ubuntu Server the same agent is installed and controlled with commands. The service is named `compute-node`. The model does not occupy the graphics card by itself. It is loaded when the connected node picks up a chat message.

`nvidia-smi` must already show the graphics card. The program does not install the NVIDIA driver.

The program file is `https://api.nshno.tech/node/compute-node-linux`. It is the Linux build of the same version as the Windows program. `compute-node.exe` will not run on Ubuntu.

## Install

```bash
curl -fL -o compute-node-linux https://api.nshno.tech/node/compute-node-linux
chmod +x compute-node-linux
sudo ./compute-node-linux setup --code <network code>
```

`setup` does what the Windows window does:

- installs Ollama 0.34.2 when it is missing and starts its service;
- installs llama.cpp b12167 (`llama-server` and `ggml-rpc-server`) into `~/.compute-network/llama`, CUDA 13.4 or 12.8 according to `nvidia-smi`;
- enrolls the node and downloads the whole model;
- copies the program to `/usr/local/bin/compute-node` and enables a systemd service.

Running `setup` again does not create a second node once `/etc/compute-network/agent.json` exists.

The network code is needed only when the owner has turned it on. The person who has the code puts it in the command.

## Connection data

At the end of `setup` the program writes a note and prints its path:

```text
/etc/compute-network/connection.txt
```

The file holds the base URL, the model name, and the API key, plus examples for clients. The full key is not printed on screen. Open the note with:

```bash
sudo cat /etc/compute-network/connection.txt
```

Those three fields go into the chat program on the computer where messages are written. The harness on the server itself is filled in only when it is already installed there. A normal Ubuntu Server does not have it, so the address and the key are copied into the harness on the computer used for chat.

Running `setup` again does not issue a new key. The node is already enrolled, and the note stays as it is.

## Commands

| Command | What it does |
|---|---|
| `sudo compute-node start` | takes jobs again. The graphics card stays empty for now |
| `sudo compute-node stop` | leaves the network and unloads the model from the graphics card |
| `sudo compute-node pause` | stops taking new jobs and unloads the model; the service keeps running |
| `sudo compute-node resume` | allows jobs again |
| `compute-node status` | shows the configuration without printing the whole secret |
| `sudo compute-node update` | downloads a newer Linux build and restarts the service |
| `compute-node version` | prints the version |

`stop` is the Disconnect button. It stops Ollama's llama-server and the layer-group processes from our llama.cpp build. A `llama-server` started somewhere else is left alone.

## After start

With one computer, Ollama runs the whole model. With two or more computers that have llama.cpp and the model file, the platform assigns continuous layer ranges by itself. A stage does not answer chat: the leading computer claims the messages. The full description is in [how-it-works.en.md](how-it-works.en.md).
