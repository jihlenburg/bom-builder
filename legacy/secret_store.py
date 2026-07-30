"""Simple runtime secret loading for local CLI use.

Python does not have a direct equivalent of Doxygen configuration files for
this kind of project, so the "Pythonic" approach is explicit module and API
documentation plus a tiny, transparent implementation. Secret resolution is
intentionally boring:

1. inherited process environment variables
2. a local ``.env`` file loaded through :mod:`python-dotenv`

The local ``.env`` file is loaded without overriding existing process
environment variables. That keeps one-shot shell overrides such as
``BOM_BUILDER_CACHE_DB=/tmp/... python main.py ...`` working as expected while
still providing a simple default local developer configuration. The module
does not write secrets, decrypt them, or talk to an external secrets backend.
Its only job is to map well-known logical secret names to the environment
variables that hold them.
"""

import os
import re
from dataclasses import dataclass

from dotenv import find_dotenv, load_dotenv

load_dotenv(find_dotenv(usecwd=True), override=False)


@dataclass(frozen=True)
class SecretSpec:
    """Metadata describing one supported runtime secret.

    Attributes
    ----------
    name:
        Stable internal identifier used by the application code.
    env_var:
        Environment-variable name expected to hold the secret value.
    description:
        Human-readable description used by documentation and future tooling.
    """

    name: str
    env_var: str
    description: str


SECRET_SPECS: dict[str, SecretSpec] = {
    "mouser_api_key": SecretSpec(
        name="mouser_api_key",
        env_var="MOUSER_API_KEY",
        description="Mouser Search API key",
    ),
    "mouser_api_keys": SecretSpec(
        name="mouser_api_keys",
        env_var="MOUSER_API_KEYS",
        description="Comma-separated Mouser Search API keys in priority order",
    ),
    "openai_api_key": SecretSpec(
        name="openai_api_key",
        env_var="OPENAI_API_KEY",
        description="OpenAI API key",
    ),
    "digikey_client_id": SecretSpec(
        name="digikey_client_id",
        env_var="DIGIKEY_CLIENT_ID",
        description="Digi-Key OAuth client ID",
    ),
    "digikey_client_secret": SecretSpec(
        name="digikey_client_secret",
        env_var="DIGIKEY_CLIENT_SECRET",
        description="Digi-Key OAuth client secret",
    ),
    "digikey_account_id": SecretSpec(
        name="digikey_account_id",
        env_var="DIGIKEY_ACCOUNT_ID",
        description="Digi-Key Account ID for account-aware product and pricing calls",
    ),
    "ti_store_api_key": SecretSpec(
        name="ti_store_api_key",
        env_var="TI_STORE_API_KEY",
        description="TI Store API client key",
    ),
    "ti_store_api_secret": SecretSpec(
        name="ti_store_api_secret",
        env_var="TI_STORE_API_SECRET",
        description="TI Store API client secret",
    ),
    "ti_product_api_key": SecretSpec(
        name="ti_product_api_key",
        env_var="TI_PRODUCT_API_KEY",
        description="Legacy TI Product Information API client key",
    ),
    "ti_product_api_secret": SecretSpec(
        name="ti_product_api_secret",
        env_var="TI_PRODUCT_API_SECRET",
        description="Legacy TI Product Information API client secret",
    ),
}


def list_secret_specs() -> tuple[SecretSpec, ...]:
    """Return all supported runtime secret specifications.

    Returns
    -------
    tuple[SecretSpec, ...]
        Immutable view of the secret registry so callers can inspect supported
        keys without mutating module state.
    """
    return tuple(SECRET_SPECS.values())


def get_secret_spec(name: str) -> SecretSpec:
    """Look up the metadata entry for a known logical secret name.

    Parameters
    ----------
    name:
        Internal secret identifier such as ``"mouser_api_key"``.

    Returns
    -------
    SecretSpec
        The matching registry record.

    Raises
    ------
    KeyError
        If the caller requests an unknown secret name.
    """
    try:
        return SECRET_SPECS[name]
    except KeyError as e:
        raise KeyError(f"Unknown secret '{name}'") from e


def get_secret(name: str, default: str = "") -> str:
    """Resolve a configured secret value from the current process environment.

    Parameters
    ----------
    name:
        Logical secret name defined in :data:`SECRET_SPECS`.
    default:
        Value returned when the secret is unset or empty.

    Returns
    -------
    str
        The stripped secret value after local ``.env`` loading has been applied,
        or ``default`` when no usable value is available.
    """
    spec = get_secret_spec(name)
    value = os.getenv(spec.env_var, "").strip()
    return value or default


def get_secret_values(name: str) -> tuple[str, ...]:
    """Resolve a secret into a tuple of one or more configured values.

    Parameters
    ----------
    name:
        Logical secret name defined in :data:`SECRET_SPECS`.

    Returns
    -------
    tuple[str, ...]
        Non-empty stripped tokens parsed from the configured environment value.

    Notes
    -----
    This is primarily intended for secrets that naturally support a fallback
    chain, such as multiple API keys listed in priority order.
    """
    raw = get_secret(name)
    if not raw:
        return ()
    return tuple(token.strip() for token in re.split(r"[,;\n]+", raw) if token.strip())
