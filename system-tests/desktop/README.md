# Desktop System Checks

These checks validate that the generated `wg0.conf` is usable on real desktop clients.

## Linux (wg-quick)
1. Generate a config:

   ```bash
   pia-wg-config generate --username YOU --password SECRET --region uk_manchester --outfile wg0.conf
   ```

2. Run the verification script:

   ```bash
   sudo ./linux/verify.sh ./wg0.conf
   ```

## Windows (WireGuard for Windows)
1. If PIA rejects the inbox Windows curl, point `CURL_PATH` at an OpenSSL-linked curl binary:

   ```powershell
   $env:CURL_PATH = "C:\tools\curl-openssl\curl.exe"
   ```

2. Generate a config:

   ```powershell
   .\pia-wg-config.exe generate --username YOU --password SECRET --region uk_manchester --outfile wg0.conf
   ```

3. Import `wg0.conf` into WireGuard for Windows and activate.

4. Run the verification script:

   ```powershell
   powershell -ExecutionPolicy Bypass -File .\windows\verify.ps1
   ```
