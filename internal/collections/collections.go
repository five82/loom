// Package collections defines the hand-picked shelves Loom groups movies into.
//
// TMDB's own belongs_to_collection is not used. For this library it splits Star
// Trek across three collections, omits Rogue One from Star Wars, separates the
// Wolverine films from X-Men, and offers director grab-bags like a "Quentin
// Tarantino Collection" holding only two of the seven Tarantino films here. It
// also reports a collection for 55 movies that are the only member owned, which
// is noise rather than a shelf. Curating the list by hand costs one edit per new
// movie and gets shelves TMDB cannot express at all.
package collections

// Collection is one shelf. Membership is by TMDB id rather than title and year
// because an id survives a re-match or a title correction, and because two
// owned movies can share a title (Scream 1996 and 2022).
type Collection struct {
	Slug    string
	Title   string
	TMDBIDs []int64
}

// All lists every shelf in display order. Only movies actually in the catalog
// resolve, so an id here for a film that is not owned simply does not appear,
// and a shelf that resolves to fewer than two movies is not served at all.
var All = []Collection{
	// Franchises. Alphabetical browse order scatters every one of these.
	{Slug: "star-wars", Title: "Star Wars", TMDBIDs: []int64{
		11,     // Star Wars (1977)
		1891,   // The Empire Strikes Back (1980)
		1892,   // Return of the Jedi (1983)
		140607, // Star Wars: The Force Awakens (2015)
		330459, // Rogue One: A Star Wars Story (2016)
	}},
	{Slug: "star-trek", Title: "Star Trek", TMDBIDs: []int64{
		152,   // Star Trek: The Motion Picture (1979)
		154,   // Star Trek II: The Wrath of Khan (1982)
		157,   // Star Trek III: The Search for Spock (1984)
		168,   // Star Trek IV: The Voyage Home (1986)
		174,   // Star Trek VI: The Undiscovered Country (1991)
		193,   // Star Trek: Generations (1994)
		199,   // Star Trek: First Contact (1996)
		13475, // Star Trek (2009)
	}},
	{Slug: "indiana-jones", Title: "Indiana Jones", TMDBIDs: []int64{
		85, // Raiders of the Lost Ark (1981)
		87, // Indiana Jones and the Temple of Doom (1984)
		89, // Indiana Jones and the Last Crusade (1989)
	}},
	{Slug: "james-bond", Title: "James Bond", TMDBIDs: []int64{
		36557,  // Casino Royale (2006)
		10764,  // Quantum of Solace (2008)
		37724,  // Skyfall (2012)
		206647, // Spectre (2015)
		370172, // No Time to Die (2021)
	}},
	{Slug: "x-men", Title: "X-Men", TMDBIDs: []int64{
		36657,  // X-Men (2000)
		36658,  // X2 (2003)
		49538,  // X-Men: First Class (2011)
		76170,  // The Wolverine (2013)
		263115, // Logan (2017)
	}},
	{Slug: "dark-knight", Title: "The Dark Knight Trilogy", TMDBIDs: []int64{
		272,   // Batman Begins (2005)
		155,   // The Dark Knight (2008)
		49026, // The Dark Knight Rises (2012)
	}},
	{Slug: "pink-panther", Title: "The Pink Panther", TMDBIDs: []int64{
		936,   // The Pink Panther (1963)
		1594,  // A Shot in the Dark (1964)
		11843, // The Return of the Pink Panther (1975)
		12268, // The Pink Panther Strikes Again (1976)
		6081,  // Revenge of the Pink Panther (1978)
	}},
	{Slug: "naked-gun", Title: "The Naked Gun", TMDBIDs: []int64{
		37136, // The Naked Gun: From the Files of Police Squad! (1988)
		37137, // The Naked Gun 2 1/2: The Smell of Fear (1991)
		36593, // Naked Gun 33 1/3: The Final Insult (1994)
	}},
	{Slug: "mission-impossible", Title: "Mission: Impossible", TMDBIDs: []int64{
		56292,  // Mission: Impossible - Ghost Protocol (2011)
		177677, // Mission: Impossible - Rogue Nation (2015)
		353081, // Mission: Impossible - Fallout (2018)
	}},
	// Toy Story earns a shelf despite sorting adjacently: it is the only way to
	// put the Toons shorts next to the features, because they live in a
	// different library.
	{Slug: "toy-story", Title: "Toy Story", TMDBIDs: []int64{
		862,    // Toy Story (1995)
		863,    // Toy Story 2 (1999)
		10193,  // Toy Story 3 (2010)
		77887,  // Hawaiian Vacation (2011)
		82424,  // Small Fry (2011)
		130925, // Partysaurus Rex (2012)
		213121, // Toy Story of Terror! (2013)
		301528, // Toy Story 4 (2019)
	}},
	{Slug: "the-godfather", Title: "The Godfather", TMDBIDs: []int64{
		238, // The Godfather (1972)
		240, // The Godfather Part II (1974)
	}},
	{Slug: "kill-bill", Title: "Kill Bill", TMDBIDs: []int64{
		24,     // Kill Bill: Vol. 1 (2003)
		393,    // Kill Bill: Vol. 2 (2004)
		414419, // Kill Bill: The Whole Bloody Affair (2011)
	}},
	{Slug: "deadpool", Title: "Deadpool", TMDBIDs: []int64{
		293660, // Deadpool (2016)
		383498, // Deadpool 2 (2018)
	}},
	{Slug: "blade-runner", Title: "Blade Runner", TMDBIDs: []int64{
		78,     // Blade Runner (1982)
		335984, // Blade Runner 2049 (2017)
	}},
	{Slug: "bill-and-ted", Title: "Bill & Ted", TMDBIDs: []int64{
		1648, // Bill & Ted's Excellent Adventure (1989)
		1649, // Bill & Ted's Bogus Journey (1991)
	}},
	{Slug: "scream", Title: "Scream", TMDBIDs: []int64{
		4232,   // Scream (1996)
		646385, // Scream (2022)
	}},
	{Slug: "hunger-games", Title: "The Hunger Games", TMDBIDs: []int64{
		70160,  // The Hunger Games (2012)
		101299, // The Hunger Games: Catching Fire (2013)
	}},
	{Slug: "vacation", Title: "National Lampoon's Vacation", TMDBIDs: []int64{
		11153, // National Lampoon's Vacation (1983)
		5825,  // National Lampoon's Christmas Vacation (1989)
	}},

	// Curated shelves. TMDB has no equivalent for any of these.
	{Slug: "pixar", Title: "Pixar", TMDBIDs: []int64{
		13928,  // Knick Knack (1989)
		862,    // Toy Story (1995)
		9487,   // A Bug's Life (1998)
		863,    // Toy Story 2 (1999)
		12,     // Finding Nemo (2003)
		15302,  // The Pixar Story (2007)
		10681,  // WALL-E (2008)
		13413,  // BURN-E (2008)
		13042,  // Presto (2008)
		14160,  // Up (2009)
		24589,  // Dug's Special Mission (2009)
		24480,  // Partly Cloudy (2009)
		10193,  // Toy Story 3 (2010)
		40619,  // Day & Night (2010)
		77887,  // Hawaiian Vacation (2011)
		82424,  // Small Fry (2011)
		62177,  // Brave (2012)
		83564,  // La luna (2012)
		130925, // Partysaurus Rex (2012)
		141423, // The Legend of Mor'du (2012)
		213121, // Toy Story of Terror! (2013)
		150540, // Inside Out (2015)
		354912, // Coco (2017)
		260513, // Incredibles 2 (2018)
		301528, // Toy Story 4 (2019)
	}},
	{Slug: "disney-animation", Title: "Disney Animation", TMDBIDs: []int64{
		11360,  // Dumbo (1941)
		11224,  // Cinderella (1950)
		12230,  // One Hundred and One Dalmatians (1961)
		10144,  // The Little Mermaid (1989)
		10020,  // Beauty and the Beast (1991)
		13053,  // Bolt (2008)
		82690,  // Wreck-It Ralph (2012)
		140420, // Paperman (2012)
		269149, // Zootopia (2016)
	}},
	{Slug: "tarantino", Title: "Quentin Tarantino", TMDBIDs: []int64{
		680,    // Pulp Fiction (1994)
		184,    // Jackie Brown (1997)
		24,     // Kill Bill: Vol. 1 (2003)
		393,    // Kill Bill: Vol. 2 (2004)
		414419, // Kill Bill: The Whole Bloody Affair (2011)
		68718,  // Django Unchained (2012)
		273248, // The Hateful Eight (2015)
		466272, // Once Upon a Time... in Hollywood (2019)
	}},
	{Slug: "world-war-ii", Title: "World War II", TMDBIDs: []int64{
		857,    // Saving Private Ryan (1998)
		613,    // Downfall (2004)
		1251,   // Letters from Iwo Jima (2006)
		324786, // Hacksaw Ridge (2016)
		399404, // Darkest Hour (2017)
		374720, // Dunkirk (2017)
	}},
	{Slug: "spaceflight", Title: "Spaceflight", TMDBIDs: []int64{
		9549,   // The Right Stuff (1983)
		568,    // Apollo 13 (1995)
		49047,  // Gravity (2013)
		286217, // The Martian (2015)
		381284, // Hidden Figures (2016)
		369972, // First Man (2018)
	}},
	{Slug: "view-askew", Title: "View Askewniverse", TMDBIDs: []int64{
		2293, // Mallrats (1995)
		1832, // Dogma (1999)
		2294, // Jay and Silent Bob Strike Back (2001)
	}},

	// Directors. Deliberately limited to a few whose work is well represented
	// here; this is not meant to grow into a shelf per credit.
	{Slug: "spielberg", Title: "Steven Spielberg", TMDBIDs: []int64{
		85,     // Raiders of the Lost Ark (1981)
		601,    // E.T. the Extra-Terrestrial (1982)
		87,     // Indiana Jones and the Temple of Doom (1984)
		89,     // Indiana Jones and the Last Crusade (1989)
		857,    // Saving Private Ryan (1998)
		180,    // Minority Report (2002)
		640,    // Catch Me If You Can (2002)
		612,    // Munich (2005)
		57212,  // War Horse (2011)
		296098, // Bridge of Spies (2015)
		446354, // The Post (2017)
	}},
	{Slug: "scorsese", Title: "Martin Scorsese", TMDBIDs: []int64{
		1578,   // Raging Bull (1980)
		769,    // GoodFellas (1990)
		524,    // Casino (1995)
		1422,   // The Departed (2006)
		398978, // The Irishman (2019)
	}},
	{Slug: "nolan", Title: "Christopher Nolan", TMDBIDs: []int64{
		272,    // Batman Begins (2005)
		155,    // The Dark Knight (2008)
		27205,  // Inception (2010)
		49026,  // The Dark Knight Rises (2012)
		374720, // Dunkirk (2017)
	}},
}
