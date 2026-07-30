# Digi-Key Account Setup

This guide covers the one-time setup required before BOM Builder can use
Digi-Key's account-aware Product Information V4 endpoints.

Once these credentials are configured, the normal BOM runtime automatically
lets Digi-Key compete with Mouser and selects the best confident priced offer
per BOM line.

## Why This Exists

Digi-Key's current Product Information V4 documentation increasingly expects an
`X-DIGIKEY-Account-ID` header on key pricing and product-detail endpoints.
That value is not obvious in the Digi-Key web UI, and Digi-Key's official
documented way to discover it is the `AssociatedAccounts` reference API.

The complication is that `AssociatedAccounts` uses **3-legged OAuth**, while
the normal server-to-server product/pricing flow uses **2-legged OAuth**.

So the practical setup flow is:

1. register a Digi-Key production app
2. set the OAuth callback to `https://localhost`
3. run the one-time account lookup helper
4. store the returned `DIGIKEY_ACCOUNT_ID` in `.env`
5. use normal 2-legged OAuth for later runtime calls

## Required `.env` Values

```bash
DIGIKEY_CLIENT_ID=your-digikey-client-id
DIGIKEY_CLIENT_SECRET=your-digikey-client-secret
```

After the one-time lookup succeeds, also store:

```bash
DIGIKEY_ACCOUNT_ID=your-digikey-account-id
```

For EUR pricing to an EU destination, also set:

```bash
DIGIKEY_LOCALE_SITE=DE
DIGIKEY_LOCALE_LANGUAGE=en
DIGIKEY_LOCALE_CURRENCY=EUR
DIGIKEY_LOCALE_SHIP_TO_COUNTRY=de
```

Those values tell Digi-Key to price against a Germany/EUR shipping context.

## One-Time Lookup Helper

Run the helper from the repository root:

```bash
python scripts/digikey_account_lookup.py --write-env
```

What it does:

1. prints the Digi-Key authorization URL
2. asks you to open that URL in your browser
3. asks you to paste back the final redirected URL or raw authorization code
4. exchanges the authorization code for tokens
5. calls Digi-Key `AssociatedAccounts`
6. prints the returned Account IDs
7. writes the selected `DIGIKEY_ACCOUNT_ID` to `.env` when `--write-env` is used

## Redirect URI

Use the same redirect URI in both places:

- Digi-Key app registration
- helper command line

Default:

```text
https://localhost
```

If you change it in the Digi-Key portal, pass the exact same value to the
helper:

```bash
python scripts/digikey_account_lookup.py --redirect-uri https://localhost/
```

The trailing slash matters if Digi-Key registered it that way.

## Useful Flags

Print only the authorization URL:

```bash
python scripts/digikey_account_lookup.py --print-only
```

Use explicit credentials without relying on `.env`:

```bash
python scripts/digikey_account_lookup.py \
  --client-id your-client-id \
  --client-secret your-client-secret
```

Paste a callback URL or authorization code non-interactively:

```bash
python scripts/digikey_account_lookup.py \
  --code 'https://localhost?code=abc123&state=xyz' \
  --write-env
```

Probe Digi-Key V4 quantity pricing after setup:

```bash
python scripts/digikey_probe.py --product-number P5555-ND --quantity 100
```

## Notes

- A browser error after redirecting to `https://localhost` is expected if you
  do not have a local web server running. You only need the final URL from the
  address bar.
- The helper validates OAuth `state` by default to avoid accepting the wrong
  callback.
- Digi-Key's docs are currently inconsistent between older `Customer-Id: 0`
  examples and newer `Account-ID` requirements. For Product Information V4,
  BOM Builder should treat `Account-ID` as the long-term contract and only use
  `Customer-Id: 0` as a compatibility probe during integration.
