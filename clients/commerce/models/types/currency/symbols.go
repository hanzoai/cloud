package currency

// Give the currency's symbol
func (t Type) Symbol() string {
	switch t {
	case ARS, AUD, BSD, BBD, BMD, BND, CAD, KYD, CLP, COP, XCD, SVC, FJD, GYD, HKD, LRD, MXN, NAD, NZD, SGD, SBD, SRD, USD:
		return "$"
	case BOB:
		return "$b"
	case UYU:
		return "$U"
	case EGP, FKP, GIP, LBP, SHP, GBP:
		return "£"
	case CNY, JPY:
		return "¥"
	case AFN:
		return "؋"
	case THB:
		return "฿"
	case KHR:
		return "៛"
	case CRC:
		return "₡"
	case NGN:
		return "₦"
	case KRW:
		return "₩"
	case ILS:
		return "₪"
	case VND:
		return "₫"
	case EUR:
		return "€"
	case LAK:
		return "₭"
	case MNT:
		return "₮"
	case PHP:
		return "₱"
	case UAH:
		return "₴"
	case MUR, NPR, PKR, SCR, LKR:
		return "₨"
	case QAR, SAR, YER:
		return "﷼"
	case PAB:
		return "B/."
	case BZD:
		return "BZ$"
	case NIO:
		return "C$"
	case CHF:
		return "CHF"
	case HUF:
		return "Ft"
	case AWG, ANG:
		return "ƒ"
	case PYG:
		return "Gs"
	case JMD:
		return "J$"
	case CZK:
		return "Kč"
	case BAM:
		return "KM"
	case HRK, DKK, EEK, ISK, NOK, SEK:
		return "kr"
	case HNL:
		return "L"
	case RON:
		return "lei"
	case ALL:
		return "Lek"
	case LVL:
		return "Ls"
	case LTL:
		return "Lt"
	case MZN:
		return "MT"
	case TWD:
		return "NT$"
	case BWP:
		return "P"
	case GTQ:
		return "Q"
	case ZAR:
		return "R"
	case BRL:
		return "R$"
	case DOP:
		return "RD$"
	case MYR:
		return "RM"
	case IDR:
		return "Rp"
	case SOS:
		return "S"
	case PEN:
		return "S/."
	case TTD:
		return "TT$"
	case PLN:
		return "zł"
	case MKD:
		return "ден"
	case RSD:
		return "Дин."
	case BGN, KZT, KGS, UZS:
		return "лв"
	case AZN:
		return "ман"
	case RUB:
		return "руб"
	case INR, TRY:
		return ""
	case PNT:
		return ""
	}
	return ""
}
