// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package maillang holds the copy the daemon addresses to a person, in every
// language the product speaks: transactional email and the hosted intake form.
//
// It is a struct rather than a map so a mistyped key is a compile error, not a
// blank line in someone's inbox, and so the guard test can walk the fields to
// prove no language is missing one or has dropped a format verb.
//
// Whose language is resolved per message by the caller: mail to an account
// holder uses that user's preference; an invitation follows the inviter, since
// the recipient has no account yet; mail a flow sends (an approval request and
// its outcome) follows core.Graph.Language, because it is the flow speaking.
// The support-queue notices to the operator's own staff stay English: a
// config-file address carries no language to resolve.
package maillang

import "strings"

// Messages is the full vocabulary of one language. Every field is used; a
// blank one is a bug the guard test catches.
//
// Fields carrying a %s say so in a comment, since the argument order is the
// contract between the catalogue and the caller.
type Messages struct {
	// ── Account invitation (the invitee has no account yet) ──
	InviteSubject   string
	InvitePreheader string
	InviteEyebrow   string
	InviteHeading   string
	InviteIntro     string
	InviteButton    string
	InviteExpiry    string // %s = expiry date
	InviteFooter    string

	// ── Organization invitation ──
	OrgInviteSubject   string
	OrgInvitePreheader string // %s = who invited them
	OrgInviteEyebrow   string
	OrgInviteHeading   string
	OrgInviteIntro     string // %s = who invited them
	OrgInviteButton    string
	OrgInviteExpiry    string // %s = expiry date
	OrgInviteFooter    string

	// ── Welcome, on a new account ──
	WelcomeSubject   string
	WelcomePreheader string
	WelcomeEyebrow   string
	WelcomeHeading   string
	WelcomeIntro1    string
	WelcomeIntro2    string
	WelcomeButton    string
	WelcomeOutro     string

	// ── Email verification ──
	VerifySubject   string
	VerifyPreheader string
	VerifyEyebrow   string
	VerifyHeading   string
	VerifyIntro     string
	VerifyButton    string
	VerifyExpiry    string // %s = expiry date

	// ── Password reset ──
	ResetSubject   string
	ResetPreheader string
	ResetEyebrow   string
	ResetHeading   string
	ResetIntro     string
	ResetButton    string
	ResetExpiry    string // %s = expiry date

	// ── A flow run failed ──
	FailureSubject   string // %s = flow name
	FailurePreheader string
	FailureEyebrow   string
	FailureHeading   string
	FailureIntro     string // %s = flow name
	FailureOutro     string
	FailureButton    string

	// ── An approval is waiting ──
	ApprovalSubject      string // %s = flow name
	ApprovalPreheader    string
	ApprovalEyebrow      string
	ApprovalHeading      string
	ApprovalIntro        string // %s = flow name
	ApprovalOutro        string
	ApprovalShareWarning string
	ApprovalOpenLink     string // button, signed link
	ApprovalOpenInbox    string // button, the Approvals page

	// ── An approval was decided ──
	DecidedEyebrow           string
	DecidedButton            string
	DecidedApprovedSubject   string // %s = flow name
	DecidedRejectedSubject   string // %s = flow name
	DecidedApprovedPreheader string // %s = flow name
	DecidedRejectedPreheader string // %s = flow name
	DecidedApprovedHeading   string
	DecidedRejectedHeading   string
	DecidedApprovedIntro     string // %s = who, %s = flow name
	DecidedRejectedIntro     string // %s = who, %s = flow name
	DecidedApprovedOutro     string
	DecidedRejectedOutro     string
	DecidedApprovedValue     string // the Decision fact's value
	DecidedRejectedValue     string
	DecidedAnonymous         string // stands in for a nameless approver

	// ── Support: we answered you / we closed it ──
	SupportRepliedSubject    string // %s = ticket subject
	SupportRepliedPreheader  string
	SupportEyebrow           string
	SupportRepliedHeading    string
	SupportRepliedIntro      string // %s = ticket subject
	SupportRepliedOutro      string
	SupportButton            string
	SupportResolvedSubject   string // %s = ticket subject
	SupportResolvedPreheader string
	SupportResolvedHeading   string
	SupportResolvedIntro     string // %s = ticket subject
	SupportResolvedOutro     string
	// The reminder: support answered and the customer never opened it. Sent
	// once per waiting period by the nudge sweeper, not on every reply — that
	// mail already exists.
	SupportWaitingSubject   string // %s = ticket subject
	SupportWaitingPreheader string
	SupportWaitingHeading   string
	SupportWaitingIntro     string // %s = ticket subject
	SupportWaitingOutro     string

	// ── Fact labels, shared by the notices above ──
	FactFlow       string
	FactRun        string
	FactStep       string
	FactError      string
	FactFinishedAt string
	FactDecision   string
	FactDecidedBy  string
	FactComment    string

	// ── The hosted intake form ──
	// Not email, but the same rule and the same resolution: a form is the
	// FLOW speaking to a visitor, so it follows core.Graph.Language. It is
	// also the only surface of the product a stranger ever sees.
	FormSubmit      string // the submit button
	FormThanksTitle string // bolded lead on the confirmation
	FormThanksBody  string
	FormErrorTitle  string // bolded lead on either error banner
	FormErrorRetry  string // transient — their input is still in the fields
	FormErrorClosed string // the form can't receive; not the visitor's fault
	FormGoneTitle   string // there is no form at this URL to show
	FormGoneBody    string
	// FormHoneypot labels the hidden anti-bot input. Nobody sees it — it sits
	// off-screen — but a screen reader reaching it reads this out, so it is the
	// flow speaking to a visitor like every other string here.
	FormHoneypot string
}

// English is the source language and the fallback for anything else.
var English = Messages{
	InviteSubject:   "You're invited to Dazyflow",
	InvitePreheader: "Create your account to get started.",
	InviteEyebrow:   "Invitation",
	InviteHeading:   "Create your Dazyflow account",
	InviteIntro:     "You've been invited to create an account on Dazyflow. Set a password and you're in.",
	InviteButton:    "Set your password",
	InviteExpiry:    "This link expires %s. If you weren't expecting it, you can ignore this email.",
	InviteFooter:    "You're receiving this because someone invited you to Dazyflow.",

	OrgInviteSubject:   "You're invited to Dazyflow",
	OrgInvitePreheader: "%s invited you to join their organization.",
	OrgInviteEyebrow:   "Invitation",
	OrgInviteHeading:   "You've been invited",
	OrgInviteIntro:     "%s invited you to join their organization on Dazyflow, where teams build and run automations together.",
	OrgInviteButton:    "Accept invitation",
	OrgInviteExpiry:    "This invitation expires %s. If you weren't expecting it, you can ignore this email.",
	OrgInviteFooter:    "You're receiving this because someone invited you to Dazyflow.",

	WelcomeSubject:   "Welcome to Dazyflow",
	WelcomePreheader: "Your account is ready — build your first flow.",
	WelcomeEyebrow:   "Welcome",
	WelcomeHeading:   "Your account is ready",
	WelcomeIntro1:    "Welcome to Dazyflow! Everything's set up and waiting for you.",
	WelcomeIntro2:    "A flow automates a task for you — on a schedule, when a form is submitted, or when another app sends it data. Start from a template or describe what you want in plain words.",
	WelcomeButton:    "Build your first flow",
	WelcomeOutro:     "Need a hand? Just reply to this email, or open the docs from inside the app.",

	VerifySubject:   "Confirm your email",
	VerifyPreheader: "Confirm your address to finish setting up your account.",
	VerifyEyebrow:   "Confirm your email",
	VerifyHeading:   "One quick step to finish",
	VerifyIntro:     "Confirm your email address to finish setting up your Dazyflow account.",
	VerifyButton:    "Verify email address",
	VerifyExpiry:    "This link expires %s. If you didn't create a Dazyflow account, you can ignore this email.",

	ResetSubject:   "Reset your Dazyflow password",
	ResetPreheader: "Choose a new password for your account.",
	ResetEyebrow:   "Password reset",
	ResetHeading:   "Reset your password",
	ResetIntro:     "We received a request to reset the password for your Dazyflow account.",
	ResetButton:    "Choose a new password",
	ResetExpiry:    "This link expires %s. If you didn't request this, ignore this email — your password is unchanged.",

	FailureSubject:   "Flow %q failed",
	FailurePreheader: "A run of your flow failed and needs your attention.",
	FailureEyebrow:   "Run failed",
	FailureHeading:   "A flow run needs your attention",
	FailureIntro:     "Your flow “%s” failed on its last run. Here's what happened:",
	FailureOutro:     "This run won't retry on its own. Open it to see the full log and fix the cause.",
	FailureButton:    "View run details",

	ApprovalSubject:      "Approval needed: %s",
	ApprovalPreheader:    "A flow has paused and is waiting for your decision.",
	ApprovalEyebrow:      "Approval needed",
	ApprovalHeading:      "A flow is waiting on you",
	ApprovalIntro:        "The flow “%s” has paused and needs someone to decide before it can carry on.",
	ApprovalOutro:        "Whoever decides first resolves it — we'll email everyone the outcome.",
	ApprovalShareWarning: "Anyone with this link can approve or reject, so please don't forward it.",
	ApprovalOpenLink:     "Open the approval",
	ApprovalOpenInbox:    "Open Approvals",

	DecidedEyebrow:           "Decision made",
	DecidedButton:            "View run details",
	DecidedApprovedSubject:   "Approved: %s",
	DecidedRejectedSubject:   "Rejected: %s",
	DecidedApprovedPreheader: "The approval on “%s” was approved.",
	DecidedRejectedPreheader: "The approval on “%s” was rejected.",
	DecidedApprovedHeading:   "The approval was approved",
	DecidedRejectedHeading:   "The approval was rejected",
	DecidedApprovedIntro:     "%s approved the pending approval on “%s”.",
	DecidedRejectedIntro:     "%s rejected the pending approval on “%s”.",
	DecidedApprovedOutro:     "The flow has resumed and is running the steps after the approval.",
	DecidedRejectedOutro:     "Nothing further is needed from you — this is just so you know it's handled.",
	DecidedApprovedValue:     "Approved",
	DecidedRejectedValue:     "Rejected",
	DecidedAnonymous:         "someone with the approval link",

	SupportRepliedSubject:    "Support replied: %s",
	SupportRepliedPreheader:  "Support has answered your ticket.",
	SupportEyebrow:           "Support",
	SupportRepliedHeading:    "Support replied to your ticket",
	SupportRepliedIntro:      "Someone from support answered “%s”.",
	SupportRepliedOutro:      "Open the ticket to read the full reply and respond.",
	SupportButton:            "View ticket",
	SupportResolvedSubject:   "Resolved: %s",
	SupportResolvedPreheader: "Your support ticket was marked resolved.",
	SupportResolvedHeading:   "Your ticket was marked resolved",
	SupportResolvedIntro:     "Support marked “%s” resolved.",
	SupportResolvedOutro:     "If it isn't actually fixed, reply on the ticket and it reopens.",
	SupportWaitingSubject:    "Still waiting for you: %s",
	SupportWaitingPreheader:  "Support answered your ticket and is waiting on you.",
	SupportWaitingHeading:    "Support is waiting for your reply",
	SupportWaitingIntro:      "Support answered “%s” and hasn't heard back.",
	SupportWaitingOutro:      "Open the ticket to read the reply. If you no longer need help, closing it tells support to stop.",

	FactFlow:       "Flow",
	FactRun:        "Run",
	FactStep:       "Step",
	FactError:      "Error",
	FactFinishedAt: "Finished at",
	FactDecision:   "Decision",
	FactDecidedBy:  "Decided by",
	FactComment:    "Comment",

	FormSubmit:      "Submit",
	FormThanksTitle: "Thanks!",
	FormThanksBody:  "Your submission was received.",
	FormErrorTitle:  "Something went wrong",
	FormErrorRetry:  "Your details are still in the form below — please try again.",
	FormErrorClosed: "This form can't accept submissions right now. Please try again later, or get in touch another way.",
	FormHoneypot:    "Leave this field empty",
	FormGoneTitle:   "This form isn't available.",
	FormGoneBody:    "The link may be out of date, or the form may not be live yet. If someone sent you here, let them know.",
}

// Swedish. Written as Swedish rather than word-for-word from the English: the
// product name stays as it is, and the decided-approval sentences are whole
// sentences per outcome because "approved"/"rejected" inflect differently in
// Swedish ("godkände"/"avslog" as verbs, "godkänd"/"avslagen" as adjectives) —
// a template with a verb slotted into it can only be right in one of them.
var Swedish = Messages{
	InviteSubject:   "Du är inbjuden till Dazyflow",
	InvitePreheader: "Skapa ditt konto för att komma igång.",
	InviteEyebrow:   "Inbjudan",
	InviteHeading:   "Skapa ditt Dazyflow-konto",
	InviteIntro:     "Du har blivit inbjuden att skapa ett konto på Dazyflow. Välj ett lösenord och du är inne.",
	InviteButton:    "Välj ditt lösenord",
	InviteExpiry:    "Länken går ut den %s. Om du inte väntade dig det här kan du bortse från mejlet.",
	InviteFooter:    "Du får det här mejlet eftersom någon har bjudit in dig till Dazyflow.",

	OrgInviteSubject:   "Du är inbjuden till Dazyflow",
	OrgInvitePreheader: "%s har bjudit in dig till sin organisation.",
	OrgInviteEyebrow:   "Inbjudan",
	OrgInviteHeading:   "Du har blivit inbjuden",
	OrgInviteIntro:     "%s har bjudit in dig till sin organisation på Dazyflow, där team bygger och kör automatiseringar tillsammans.",
	OrgInviteButton:    "Acceptera inbjudan",
	OrgInviteExpiry:    "Inbjudan går ut den %s. Om du inte väntade dig den kan du bortse från mejlet.",
	OrgInviteFooter:    "Du får det här mejlet eftersom någon har bjudit in dig till Dazyflow.",

	WelcomeSubject:   "Välkommen till Dazyflow",
	WelcomePreheader: "Ditt konto är klart — bygg ditt första flöde.",
	WelcomeEyebrow:   "Välkommen",
	WelcomeHeading:   "Ditt konto är klart",
	WelcomeIntro1:    "Välkommen till Dazyflow! Allt är på plats och väntar på dig.",
	WelcomeIntro2:    "Ett flöde utför en uppgift för dig — enligt ett schema, när ett formulär skickas in eller när en annan app skickar data. Börja från en mall eller beskriv vad du vill ha med egna ord.",
	WelcomeButton:    "Bygg ditt första flöde",
	WelcomeOutro:     "Behöver du hjälp? Svara bara på det här mejlet, eller öppna dokumentationen inifrån appen.",

	VerifySubject:   "Bekräfta din e-postadress",
	VerifyPreheader: "Bekräfta din adress för att göra klart ditt konto.",
	VerifyEyebrow:   "Bekräfta din e-post",
	VerifyHeading:   "Ett snabbt steg kvar",
	VerifyIntro:     "Bekräfta din e-postadress för att göra klart ditt Dazyflow-konto.",
	VerifyButton:    "Bekräfta e-postadressen",
	VerifyExpiry:    "Länken går ut den %s. Om du inte har skapat något Dazyflow-konto kan du bortse från mejlet.",

	ResetSubject:   "Återställ ditt Dazyflow-lösenord",
	ResetPreheader: "Välj ett nytt lösenord till ditt konto.",
	ResetEyebrow:   "Återställning av lösenord",
	ResetHeading:   "Återställ ditt lösenord",
	ResetIntro:     "Vi har fått en begäran om att återställa lösenordet till ditt Dazyflow-konto.",
	ResetButton:    "Välj ett nytt lösenord",
	ResetExpiry:    "Länken går ut den %s. Om det inte var du som begärde det kan du bortse från mejlet — ditt lösenord är oförändrat.",

	FailureSubject:   "Flödet %q misslyckades",
	FailurePreheader: "En körning av ditt flöde misslyckades och behöver din uppmärksamhet.",
	FailureEyebrow:   "Körningen misslyckades",
	FailureHeading:   "En flödeskörning behöver din uppmärksamhet",
	FailureIntro:     "Ditt flöde ”%s” misslyckades vid den senaste körningen. Så här gick det till:",
	FailureOutro:     "Körningen görs inte om av sig själv. Öppna den för att se hela loggen och åtgärda orsaken.",
	FailureButton:    "Visa körningen",

	ApprovalSubject:      "Godkännande behövs: %s",
	ApprovalPreheader:    "Ett flöde har pausat och väntar på ditt beslut.",
	ApprovalEyebrow:      "Godkännande behövs",
	ApprovalHeading:      "Ett flöde väntar på dig",
	ApprovalIntro:        "Flödet ”%s” har pausat och behöver att någon fattar ett beslut innan det kan fortsätta.",
	ApprovalOutro:        "Den som beslutar först avgör saken — vi mejlar utfallet till alla.",
	ApprovalShareWarning: "Vem som helst med den här länken kan godkänna eller avslå, så vidarebefordra den inte.",
	ApprovalOpenLink:     "Öppna godkännandet",
	ApprovalOpenInbox:    "Öppna Godkännanden",

	DecidedEyebrow:           "Beslut fattat",
	DecidedButton:            "Visa körningen",
	DecidedApprovedSubject:   "Godkänt: %s",
	DecidedRejectedSubject:   "Avslaget: %s",
	DecidedApprovedPreheader: "Godkännandet på ”%s” blev godkänt.",
	DecidedRejectedPreheader: "Godkännandet på ”%s” blev avslaget.",
	DecidedApprovedHeading:   "Godkännandet blev godkänt",
	DecidedRejectedHeading:   "Godkännandet blev avslaget",
	DecidedApprovedIntro:     "%s godkände det väntande godkännandet på ”%s”.",
	DecidedRejectedIntro:     "%s avslog det väntande godkännandet på ”%s”.",
	DecidedApprovedOutro:     "Flödet har återupptagits och kör stegen efter godkännandet.",
	DecidedRejectedOutro:     "Inget mer behövs från dig — det här är bara så att du vet att det är hanterat.",
	DecidedApprovedValue:     "Godkänt",
	DecidedRejectedValue:     "Avslaget",
	DecidedAnonymous:         "någon med godkännandelänken",

	SupportRepliedSubject:    "Supporten har svarat: %s",
	SupportRepliedPreheader:  "Supporten har svarat på ditt ärende.",
	SupportEyebrow:           "Support",
	SupportRepliedHeading:    "Supporten har svarat på ditt ärende",
	SupportRepliedIntro:      "Någon i supporten har svarat på ”%s”.",
	SupportRepliedOutro:      "Öppna ärendet för att läsa hela svaret och svara tillbaka.",
	SupportButton:            "Visa ärendet",
	SupportResolvedSubject:   "Löst: %s",
	SupportResolvedPreheader: "Ditt supportärende har markerats som löst.",
	SupportResolvedHeading:   "Ditt ärende har markerats som löst",
	SupportResolvedIntro:     "Supporten har markerat ”%s” som löst.",
	SupportResolvedOutro:     "Om det inte faktiskt är löst svarar du i ärendet, så öppnas det igen.",
	SupportWaitingSubject:    "Väntar fortfarande på dig: %s",
	SupportWaitingPreheader:  "Supporten har svarat på ditt ärende och väntar på dig.",
	SupportWaitingHeading:    "Supporten väntar på ditt svar",
	SupportWaitingIntro:      "Supporten har svarat på ”%s” och har inte hört något sedan dess.",
	SupportWaitingOutro:      "Öppna ärendet för att läsa svaret. Om du inte behöver hjälp längre kan du stänga det, så vet supporten att den kan släppa det.",

	FactFlow:       "Flöde",
	FactRun:        "Körning",
	FactStep:       "Steg",
	FactError:      "Fel",
	FactFinishedAt: "Avslutades",
	FactDecision:   "Beslut",
	FactDecidedBy:  "Beslutat av",
	FactComment:    "Kommentar",

	FormSubmit:      "Skicka",
	FormThanksTitle: "Tack!",
	FormThanksBody:  "Vi har tagit emot ditt svar.",
	FormErrorTitle:  "Något gick fel",
	FormErrorRetry:  "Dina uppgifter finns kvar i formuläret nedan — försök igen.",
	FormErrorClosed: "Det här formuläret kan inte ta emot svar just nu. Försök igen senare, eller kontakta oss på annat sätt.",
	FormHoneypot:    "Lämna det här fältet tomt",
	FormGoneTitle:   "Formuläret är inte tillgängligt.",
	FormGoneBody:    "Länken kan vara gammal, eller så är formuläret inte publicerat än. Hör gärna av dig till den som skickade dig hit.",
}

// For resolves a language code to its messages. Only the primary subtag is
// read ("sv-SE" is Swedish), and anything unknown — including empty — is
// English, so a language nobody has translated yet degrades to the source
// text rather than to blanks. Mirrors datenames.For, deliberately: the two are
// always resolved from the same code.
func For(code string) Messages {
	switch Primary(code) {
	case "sv":
		return Swedish
	default:
		return English
	}
}

// Primary reports the language code For actually resolved to — "en" or "sv",
// never a region ("sv-SE" → "sv") and never empty. It exists so a caller that
// must NAME the language it is writing in, rather than just look up words,
// cannot disagree with the catalogue For handed back: the hosted form's
// `<html lang>` is derived from this, so the attribute and the copy on the page
// are always the same language.
func Primary(code string) string {
	primary := strings.ToLower(strings.TrimSpace(code))
	if i := strings.IndexAny(primary, "-_"); i >= 0 {
		primary = primary[:i]
	}
	switch primary {
	case "sv":
		return "sv"
	default:
		return "en"
	}
}
