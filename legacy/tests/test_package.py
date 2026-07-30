"""Tests for package extraction logic."""

import sys
from pathlib import Path

import pytest

sys.path.insert(0, str(Path(__file__).parent.parent))

from package import (
    _extract_from_description,
    _extract_from_image_url,
    _extract_from_mpn,
    extract_package_info,
)


class TestExtractFromDescription:
    def test_0402_resistor(self):
        pkg, pins = _extract_from_description(
            "Thick Film Resistors - SMD General Purpose Chip Resistor 0402, 10kOhms"
        )
        assert pkg == "0402"
        assert pins == 2

    def test_soic_8(self):
        pkg, pins = _extract_from_description("CAN Transceiver SOIC-8")
        assert pkg == "SOIC-8"
        assert pins == 8

    def test_qfn_48(self):
        pkg, pins = _extract_from_description("MCU QFN-48 package")
        assert pkg == "QFN-48"
        assert pins == 48

    def test_sot23(self):
        pkg, pins = _extract_from_description("NPN transistor SOT-23")
        assert pkg == "SOT-23"
        assert pins == 3

    def test_sot23_5(self):
        pkg, pins = _extract_from_description("LDO regulator SOT-23-5")
        assert pkg == "SOT-23-5"
        assert pins == 5

    def test_leading_pin_count_sot23(self):
        pkg, pins = _extract_from_description("Temperature sensor 8-SOT-23 -40 to 125")
        assert pkg == "SOT-23-8"
        assert pins == 8

    def test_lqfp_64(self):
        pkg, pins = _extract_from_description("ARM MCU LQFP-64")
        assert pkg == "LQFP-64"
        assert pins == 64

    def test_to_92(self):
        pkg, pins = _extract_from_description("Temperature sensor TO-92")
        assert pkg == "TO-92"
        assert pins == 3

    def test_no_match(self):
        pkg, pins = _extract_from_description("Some generic description")
        assert pkg is None
        assert pins is None

    def test_empty_string(self):
        pkg, pins = _extract_from_description("")
        assert pkg is None


class TestExtractFromImageUrl:
    def test_sot23_8_from_url(self):
        url = "https://www.mouser.com/images/ti/images/ITP_TI_SOT-23-8_DCN_t.jpg"
        pkg, pins = _extract_from_image_url(url)
        assert pkg == "SOT-23-8"
        assert pins == 8

    def test_lqfp_64_underscore(self):
        url = "https://www.mouser.com/images/mouserelectronics/images/LQFP_64_t.jpg"
        pkg, pins = _extract_from_image_url(url)
        assert pkg == "LQFP-64"
        assert pins == 64

    def test_no_match(self):
        url = "https://www.mouser.com/images/generic_chip.jpg"
        pkg, pins = _extract_from_image_url(url)
        assert pkg is None

    def test_empty_url(self):
        pkg, pins = _extract_from_image_url("")
        assert pkg is None

    def test_wson_from_url(self):
        url = "https://www.mouser.com/images/texasinstruments/images/WSON-2_DQX_DSL.jpg"
        pkg, pins = _extract_from_image_url(url)
        assert pkg == "WSON-2"
        assert pins == 2


class TestExtractFromMpn:
    def test_ti_dcn_package(self):
        pkg, pins = _extract_from_mpn("TMP423AQDCNRQ1", "Texas Instruments")
        assert pkg == "SOT-23-8"
        assert pins == 8

    def test_ti_mfx_package(self):
        pkg, pins = _extract_from_mpn("LM3670MFX-3.3/NOPB", "Texas Instruments")
        assert pkg == "SOT-23-5"
        assert pins == 5

    def test_ti_short_name(self):
        pkg, pins = _extract_from_mpn("TMP423AQDCNRQ1", "TI")
        assert pkg == "SOT-23-8"

    def test_ti_soic_code(self):
        pkg, pins = _extract_from_mpn("UCC27302AQDRQ1", "TI")
        assert pkg == "SOIC-8"
        assert pins == 8

    def test_ti_x2son_code(self):
        pkg, pins = _extract_from_mpn("LP590730QDQNRQ1", "TI")
        assert pkg == "X2SON-4"
        assert pins == 4

    def test_ti_vssop_code(self):
        pkg, pins = _extract_from_mpn("TPS48000QDGXRQ1", "TI")
        assert pkg == "VSSOP-19"
        assert pins == 19

    def test_stm32_lqfp(self):
        pkg, pins = _extract_from_mpn("STM32F405RGT6", "STMicroelectronics")
        assert pkg == "LQFP-64"
        assert pins == 64

    def test_unknown_manufacturer(self):
        pkg, pins = _extract_from_mpn("UNKNOWN123", "SomeMfr")
        assert pkg is None


class TestExtractPackageInfo:
    def test_description_takes_priority(self):
        part = {
            "Description": "Resistor 0402",
            "ImagePath": "https://mouser.com/images/LQFP_64.jpg",
            "ManufacturerPartNumber": "RC0402FR",
        }
        pkg, pins = extract_package_info(part, "Yageo")
        assert pkg == "0402"
        assert pins == 2

    def test_fallback_to_image(self):
        part = {
            "Description": "Some generic part",
            "ImagePath": "https://mouser.com/images/LQFP_64_t.jpg",
            "ManufacturerPartNumber": "PARTX",
        }
        pkg, pins = extract_package_info(part, "SomeMfr")
        assert pkg == "LQFP-64"

    def test_fallback_to_mpn(self):
        part = {
            "Description": "Switching Voltage Regulators",
            "ImagePath": "https://mouser.com/images/generic.jpg",
            "ManufacturerPartNumber": "LM3670MFX-3.3/NOPB",
        }
        pkg, pins = extract_package_info(part, "Texas Instruments")
        assert pkg == "SOT-23-5"
        assert pins == 5

    def test_tmp421_tube_variant_uses_specific_sot23_8(self):
        part = {
            "Description": (
                "Board Mount Temperature Sensors AEC-Q100 Automotive "
                "1Ch Remote Temperature Sensor 8-SOT-23 -40 to 125"
            ),
            "ImagePath": (
                "https://www.mouser.com/images/texasinstruments/images/"
                "ITP_TI_SOT-23-8_DCN_t.jpg"
            ),
            "ManufacturerPartNumber": "TMP421AQDCNTQ1",
        }
        pkg, pins = extract_package_info(part, "TI")
        assert pkg == "SOT-23-8"
        assert pins == 8
