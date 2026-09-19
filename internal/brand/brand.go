// Package brand holds the one name the product is called in front of a person.
//
// "Lull" is the codebase. "Preggo Pillow" is the product, and it is what the
// landing page, the sign-in screen and the dashboard say. The two had drifted:
// the clinician's report was headed Lull, the phone remote was titled Lull,
// and the escalation call introduced itself as Lull to whoever picked up. A
// midwife reading a record from a product she was told was called something
// else has a reason to distrust the record.
package brand

// Product is the name a person sees. Nothing user-facing should hardcode it.
const Product = "Preggo Pillow"

// Tagline describes the product in one clause, for the phone call and the
// report subtitle, where there is no logo to carry the meaning.
const Tagline = "the fetal movement monitor"
