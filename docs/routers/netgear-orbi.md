# Netgear Orbi

The Orbi **always hands out itself** as the DNS server; its LAN Setup page has no field to change that. So the Orbi needs **setup B**: NetGuard takes over handing out addresses (DHCP), and the Orbi keeps doing Wi-Fi and routing. Mesh satellites keep working normally.

If your internet comes in through an ISP gateway, such as Xfinity, that gateway is normally in **bridge mode** and the Orbi is the real router. Leave the gateway alone.

The example addresses assume the Orbi is `192.168.1.1`. Check yours under **Advanced → Setup → LAN Setup** and use your own numbers.

## Steps

1. **Free up fixed addresses.**
   - In **Advanced → Setup → LAN Setup**, change *Starting IP Address* from `.2` to `.20` and apply.
   - This keeps `.2`–`.19` free for the Pi.
2. **Give the Pi its fixed address**, e.g. `192.168.1.2`. See the README, step 4.
3. **Install NetGuard** with `dhcp.enabled: false`, and test with one device.
4. **Prepare the config.** Set the `dhcp` section to:
   - `server_ip`: the Pi;
   - `router`: the Orbi;
   - range `.20`–`.254`, the same range as the Orbi, so devices keep their addresses.
5. **Turn off the Orbi's IPv6.** Go to **Advanced → Advanced Setup → IPv6** → *Disabled*. Otherwise the Orbi advertises itself as a DNS server over IPv6.
6. **Turn off Orbi features that depend on its DHCP**, if you use them: Circle / NETGEAR Smart Parental Controls, and Armor.
7. **Switch over, at a quiet time:**
   1. Untick **"Use Router as DHCP Server"** in LAN Setup and apply.
   2. Immediately set `dhcp.enabled: true`, and deploy with `-PushConfig` (`--push-config` on macOS/Linux).
   3. Reboot the Orbi from **Advanced → Advanced Home → Reboot**, so every device reconnects and gets its settings from NetGuard. Wi-Fi is down for 2–3 minutes.
8. **Check.**
   - The dashboard's DHCP page fills with devices.
   - On a computer, `ipconfig /all` (Windows) or Network settings (Mac) shows the Pi as the **DNS server** and the **DHCP server**.

**Undo:** tick "Use Router as DHCP Server" again, set `dhcp.enabled: false`, and push the config.

## Notes

- **Several satellites can report the same model name.** NetGuard keeps them as separate devices, because their MAC addresses are fixed.
- **Some Wi-Fi devices in standby, such as TVs, ignore DHCP replies sent as a broadcast.** NetGuard replies directly to each device's hardware address, as the DHCP standard asks.
