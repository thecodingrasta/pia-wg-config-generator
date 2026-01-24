# Desktop system checks

These checks validate that the generated `wg0.conf` is usable on real desktop clients.

## Linux (wg-quick)
1. Generate:
   `pia-wg-config generate -o wg0.conf -r uk_manchester USER PASS`

2. Run verification script:
   `sudo ./linux/verify.sh ./wg0.conf`

## Windows (WireGuard for Windows)
1. Define the path to OpenSSL Curl, PIAs WAF blocks the Curl Windows ships with (Schannel)...

2. Generate:
   `pia-wg-config generate -o wg0.conf -r uk_manchester USER PASS`

3. Import `wg0.conf` into WireGuard for Windows and activate.

4. Run:
   `powershell -ExecutionPolicy Bypass -File .\windows\verify.ps1`