package wire

import "strconv"

// ScaleSigned renders a signed raw value with an implied decimal exponent as a
// decimal string, exactly and without floating point. exp follows the spec's
// convention (e.g. exp = -2 means the raw value is divided by 100).
//
// The result is a minimal decimal: trailing fractional zeros and a trailing
// dot are trimmed (e.g. 11337700 with exp -2 -> "113377"). The Hyperliquid
// px/sz fields are JSON strings parsed as decimals, so trimming is lossless.
func ScaleSigned(v int64, exp int8) string {
	if v < 0 {
		return "-" + scaleMagnitude(uint64(-v), exp)
	}
	return scaleMagnitude(uint64(v), exp)
}

// ScaleUnsigned renders an unsigned raw value with an implied decimal exponent.
func ScaleUnsigned(v uint64, exp int8) string {
	return scaleMagnitude(v, exp)
}

// scaleMagnitude formats a non-negative magnitude with the given exponent.
func scaleMagnitude(v uint64, exp int8) string {
	digits := strconv.FormatUint(v, 10)

	switch {
	case exp == 0:
		return digits

	case exp > 0:
		// Append exp zeros: value is an integer with trailing zeros.
		out := make([]byte, 0, len(digits)+int(exp))
		out = append(out, digits...)
		for i := int8(0); i < exp; i++ {
			out = append(out, '0')
		}
		return string(out)

	default:
		// exp < 0: place a decimal point d digits from the right.
		d := int(-exp)
		if len(digits) <= d {
			// Need leading zeros: 0.00ddd
			frac := make([]byte, d)
			pad := d - len(digits)
			for i := 0; i < pad; i++ {
				frac[i] = '0'
			}
			copy(frac[pad:], digits)
			return trimFrac("0", string(frac))
		}
		split := len(digits) - d
		return trimFrac(digits[:split], digits[split:])
	}
}

// trimFrac joins an integer part and a fractional part, trimming trailing
// zeros from the fraction and dropping the dot entirely if nothing remains.
func trimFrac(intPart, frac string) string {
	end := len(frac)
	for end > 0 && frac[end-1] == '0' {
		end--
	}
	if end == 0 {
		return intPart
	}
	return intPart + "." + frac[:end]
}
