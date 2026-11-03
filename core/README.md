# HomeDrop core

Fast, encrypted file transfer between your devices on the same network.
This is the Go engine + CLI; the Flutter app (Linux/Android) comes next.

## Build
    cd core && go build -o bin/homedropd ./cmd/homedropd

## Use
On the receiving machine:

    ./bin/homedropd serve                 # files go to ~/Downloads/HomeDrop
    ./bin/homedropd serve --clipboard     # also copy received text to clipboard

On the sending machine:

    ./bin/homedropd list                              # who's around?
    ./bin/homedropd send --to phone ~/Videos/trip.mp4 ~/Pictures/Album
    ./bin/homedropd send --to phone --text "https://example.com"
    ./bin/homedropd send --to 192.168.1.20:53530 file.zip   # if discovery is blocked

The first time two devices talk, both show a 4-digit code — check they match,
then accept. After that the device is trusted (`serve --auto-accept` skips the
prompt for trusted devices).

## Firewall
HomeDrop uses port **53530** (TCP for transfers, UDP multicast for discovery):

    sudo ufw allow 53530

## Tests
    go test -race ./...
