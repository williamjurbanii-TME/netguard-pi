# CLAUDE.md: guide for setting up NetGuard

This file is for Claude (Claude Code). The person you're helping has probably downloaded this project to put NetGuard on their own Raspberry Pi. They may not be technical. Guide them one step at a time, run what you can yourself, and check each step before moving on.

If NetGuard is already installed (the user mentions their Pi's address, or `config.yaml` exists with DHCP settings filled in), skip to **Maintenance** at the end.

## Ground rules

- **Never print, log or commit the dashboard password**, or any password. The user types it into `config.yaml` themselves. To check it's set, grep for a non-empty value without showing it. `config.yaml` is gitignored; keep it that way.
- **Never overwrite an existing SSH key.** `~/.ssh/id_ed25519` is often someone's GitHub key. Always create a separate key file for the Pi (`~/.ssh/netguard_ed25519`) and add a `Host` entry to `~/.ssh/config`. Before writing to `~/.ssh/config`, read it and append; never replace it.
- **Only one DHCP server may run on a network.** Never enable NetGuard's DHCP until the user has confirmed the router's DHCP is off. Always give the undo step first.
- **Never suggest exposing NetGuard to the internet** (port forwarding 53/67/8080).
- **Don't run DHCP from a laptop or desktop for the whole house.** It belongs on the Pi, which is always on.
- **Explain before acting on the network.** Changing the Pi's address or the router's DHCP drops connections; say what will happen first.
- Router admin pages are the user's to click through. You give exact menu paths and ask for screenshots if unsure.

## Phase 0: what you need to know

Ask, or work out:

1. **Their computer's OS.** Windows uses `deploy\deploy.ps1` from PowerShell; macOS/Linux use `deploy/deploy.sh`.
2. **Is Go installed?** Check with `go version`. If not:
   - Windows: `winget install --id GoLang.Go -e --source winget`. The binary lands in `C:\Program Files\Go\bin`, which may need adding to PATH in your shell.
   - macOS: `brew install go`.
   - Otherwise, download it from https://go.dev/dl/.
3. **Router make and model**, and whether a separate ISP gateway exists (e.g. an Xfinity/Comcast box, often in bridge mode). This decides setup A or B. See "Choosing a setup" in the README and `docs/routers/`.
   - **Setup A:** the router's LAN/DHCP page has a DNS server field for clients. Use that; NetGuard's DHCP stays off.
   - **Setup B:** no such field (e.g. Netgear Orbi, many ISP routers and mesh systems). NetGuard does DHCP.
   - Warn them about the trap: setting only the router's **WAN/Internet** DNS to the Pi makes every query look like it came from the router, so per-device rules and monitoring break.
4. **Their LAN numbers:** the router's IP, the subnet mask and the DHCP range. Get these from the router's LAN page, or from `ipconfig` / `ifconfig` (the default gateway).

## Phase 1: SD card and SSH

1. Create the SSH key, without a passphrase so deploys don't prompt:

   ```bash
   ssh-keygen -t ed25519 -f ~/.ssh/netguard_ed25519
   ```

   On Windows the path is `C:\Users\<name>\.ssh\netguard_ed25519`. Tell them to press Enter at both passphrase prompts.
   - If `ssh-keygen` seems to "hang", it is waiting at a prompt. Passing `-f` avoids the file-name prompt.
   - Copy the public key to the clipboard:
     - Windows: `Get-Content $HOME\.ssh\netguard_ed25519.pub | Set-Clipboard`
     - macOS: `pbcopy < ~/.ssh/netguard_ed25519.pub`
2. Add this to `~/.ssh/config`, appending to the existing file:

   ```
   Host netguard netguard.local <pi-ip>
     User pi
     IdentityFile ~/.ssh/netguard_ed25519
     IdentitiesOnly yes
   ```

3. Raspberry Pi Imager:
   - Install it on Windows with `winget install --id RaspberryPiFoundation.RaspberryPiImager -e --source winget`, or download it from https://www.raspberrypi.com/software/.
   - Device: **Raspberry Pi 5** (or theirs). OS: **Raspberry Pi OS (other) → Raspberry Pi OS Lite (64-bit)**.
   - Customisation:
     - hostname `netguard`, user `pi` plus a password they'll remember, and their time zone;
     - no Wi-Fi;
     - SSH **enabled with public-key authentication**, pasting the key;
     - **Raspberry Pi Connect off**.
   - Imager can't set a static IP; that comes in phase 2.
4. They insert the card, connect **Ethernet to the main router** (not a mesh satellite if avoidable) and power on. Wait about 2 minutes, then test from your shell:

   ```bash
   ssh -o BatchMode=yes pi@netguard.local hostname
   ```

   If `netguard.local` doesn't resolve, get the IP from the router's attached-devices page. If the user re-imaged the card, remove the stale host key first with `ssh-keygen -R netguard.local`.
5. **Passwordless sudo.** Newer Raspberry Pi OS asks for the password on `sudo`, and you can't type it. Have **the user** run this in their own SSH session; it asks for the password once:

   ```bash
   echo 'pi ALL=(ALL) NOPASSWD:ALL' | sudo tee /etc/sudoers.d/010_pi-nopasswd && sudo chmod 440 /etc/sudoers.d/010_pi-nopasswd && sudo visudo -c
   ```

   Expect `parsed OK`. Verify with `ssh pi@... 'sudo -n true && echo ok'`.

## Phase 2: fixed address

1. **Pick the address.** Choose one outside the router's DHCP range, e.g. `.2`.
   - If the range starts at `.2`, have the user move the start to `.20` first, on the router's LAN page.
   - Check the address is free, e.g. with `ping`, before using it.
2. Set it over SSH. The connection drops when the address changes, so run the switch detached:

   ```bash
   ssh pi@netguard.local 'sudo nmcli con mod "Wired connection 1" ipv4.method manual ipv4.addresses <ip>/24 ipv4.gateway <router-ip> ipv4.dns "1.1.1.1 9.9.9.9" && (sudo nohup nmcli con up "Wired connection 1" >/dev/null 2>&1 &)'
   ```

   - Confirm the connection name first with `nmcli -t -f NAME,DEVICE con show --active`.
   - Use the right prefix for their subnet mask: `/24` is `255.255.255.0`.
   - The Pi uses public DNS for itself on purpose, so it can still download blocklists if NetGuard is down.
3. Reconnect at the new address and check that the gateway responds and DNS works.

## Phase 3: install

1. `cp config.example.yaml config.yaml`.
   - Fill in the `dhcp` section with their numbers (setup B), and leave `enabled: false`.
   - Ask the user to set `web.password` themselves, e.g. with `notepad config.yaml`. Then verify it's non-empty **without printing it**:

     ```bash
     grep -qE '^\s*password:\s*"[^"]+"' config.yaml
     ```
2. Install. This runs the tests, cross-compiles, uploads, and creates the systemd service:
   - Windows: `.\deploy\deploy.ps1 -Target pi@<ip> -Install`
   - macOS/Linux: `deploy/deploy.sh pi@<ip> --install`

   On Windows, if you run it with stderr redirected, ignore SSH's "Connection to … closed". The script judges success by exit code and prints `OK: NetGuard <version> is running.`
3. Verify:
   - The service is running: `ssh pi@<ip> 'systemctl is-active netguard; sudo journalctl -u netguard -n 20 --no-pager -o cat'`. Expect the lists to have loaded, "dns: listening", and the dashboard up.
   - DNS answers, from their computer:
     - `nslookup doubleclick.net <ip>` gives `0.0.0.0` or `::` (blocked);
     - `nslookup github.com <ip>` gives a real address.
   - The dashboard needs a password: `curl -s -o /dev/null -w "%{http_code}" http://<ip>:8080/api/summary` returns `401`.

## Phase 4: test with one device

The user sets one device's DNS by hand to the Pi's address.
- **Windows:** Settings → Network → DNS server assignment → Manual → IPv4 DNS `<ip>`. Then run `ipconfig /flushdns`.
- **iPhone:** Settings → Wi-Fi → ⓘ → Configure DNS → Manual.

They browse, then check the dashboard at `http://<ip>:8080` (login `admin` and their password): the device appears and ads show as blocked. Remind them to switch that device back to automatic DNS after the switch-over.

## Phase 5: switch the network over (at a quiet time)

- **Setup A:** the user sets the router's LAN/DHCP DNS server to the Pi's address, leaving no public secondary DNS. Devices pick it up as their leases renew; rebooting the router speeds that up.
- **Setup B:**
  1. Edit `config.yaml` to `dhcp.enabled: true`, but **don't push yet**.
  2. The user turns **off** the router's DHCP server and its **IPv6** (or at least its IPv6 DNS/RDNSS), and tells you when that's done.
  3. Push immediately: `deploy.ps1 -Target pi@<ip> -PushConfig` (`--push-config` on macOS/Linux).
  4. Check the log shows `dhcp: serving <range> on :67`, and `ss -ulpn` shows `:67`.
  5. The user reboots the router, so every device reconnects.
  6. Watch `journalctl -u netguard -f -o cat | grep dhcp`. Leases should appear with device names.
- Check from their computer: `ipconfig /all` should show DHCP Server and DNS Servers both equal to the Pi.
- **Undo** (keep it handy): router DHCP back on, `dhcp.enabled: false`, push the config.

Router specifics: `docs/routers/`. Add a new file there when you learn a router's quirks.

## Phase 6: tidy up with the user

On the dashboard:
- Put TVs in **TVs**, children's devices in **Kids** or **School**.
- Name unclear devices with **Edit → Name**.

To tell identical "iPhone"s apart, the user compares **Settings → Wi-Fi → ⓘ → Wi-Fi Address** on each phone with the Devices page. Recommend setting **Private Wi-Fi Address → Fixed** on kids' phones.

## Diagnostics

- **Read the database (read-only) without the dashboard password.** Python 3 and sqlite3 are on the Pi:

  ```bash
  ssh pi@<ip> 'sudo -u netguard python3 -c "import sqlite3; db=sqlite3.connect(\"file:/opt/netguard/data/netguard.db?mode=ro\", uri=True); print(db.execute(\"SELECT hostname, last_ipv4, last_seen FROM devices\").fetchall())"'
  ```

  Tables:
  - `devices`
  - `aliases` (identifier → device)
  - `queries` (ts, device_id, client_ip, domain, qtype, status, reason)
  - `groups`
  - `leases`

  Prefer read-only access. If you must edit, edit and then `sudo systemctl restart netguard` so the in-memory state reloads.
- **A device keeps re-requesting DHCP and never makes DNS queries:** it isn't receiving the replies. NetGuard unicasts to the client's MAC on Linux (an `AF_PACKET` socket, which needs `CAP_NET_RAW`, granted in the systemd unit). Check that the startup log does *not* say "replies to new clients will be broadcast".
- **A device gets an address but never makes DNS queries:** it probably has hard-coded DNS (some cameras and TVs). This is a known limit.
- **List LAN devices with discovered names:** `ssh pi@<ip> /opt/netguard/netguard -scan`.

## Maintenance

- **Deploy code changes:** `deploy.ps1 -Target pi@<ip>` (or `deploy.sh`). Undo with `-Rollback`.
- **Config changes:** edit `config.yaml`, then deploy with `-PushConfig`.
- **Code changes:** run `go vet ./...` and `go test ./...`. Keep personal details out of the repository: real MACs, IPs, names and passwords belong only in `config.yaml`. Tests use documentation addresses (`192.168.1.x`, `00:00:5e:00:53:xx`).
