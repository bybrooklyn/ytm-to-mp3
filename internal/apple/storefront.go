package apple

// storefrontIDs are Apple's numeric store-front ids for common countries, for the
// X-Apple-Store-Front header the web-playback endpoints read. Countries not listed
// fall back to the US header, which is what the web player sent before.
var storefrontIDs = map[string]string{
	"us": "143441", "gb": "143444", "ca": "143455", "au": "143460", "nz": "143461",
	"ie": "143449", "de": "143443", "fr": "143442", "nl": "143452", "be": "143446",
	"ch": "143459", "at": "143445", "se": "143456", "no": "143457", "dk": "143458",
	"fi": "143447", "es": "143454", "pt": "143453", "it": "143450", "pl": "143478",
	"jp": "143462", "kr": "143466", "mx": "143468", "br": "143503", "in": "143467",
	"sg": "143464", "hk": "143463", "tw": "143470", "za": "143472",
}

// storefrontHeader is the X-Apple-Store-Front value for a country code ("" = US).
func storefrontHeader(country string) string {
	if id, ok := storefrontIDs[country]; ok {
		return id + "-1,25"
	}
	return ""
}
