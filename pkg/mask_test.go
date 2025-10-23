package capo

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("CopyMask", func() {
	Describe("filtering", func() {
		Context("with single builder with single copy", func() {
			var masks CopyMasks
			var builders []Builder

			BeforeEach(func() {
				builders = []Builder{
					{
						Alias:    "builder",
						Pullspec: "builder-image",
						Copies: []Copy{
							{Source: []string{"/app"}, Dest: "/usr/app", Stage: FinalStage},
						},
					},
				}
				masks = NewCopyMasks(builders)
			})

			It("should include files within the app directory", func() {
				builder := builders[0]
				mask := masks.GetMask(builder)

				Expect(mask.Includes("app/file.txt")).To(BeTrue())
				Expect(mask.Includes("app")).To(BeTrue())
				Expect(mask.Includes("app/subdir/file.txt")).To(BeTrue())
			})

			It("should exclude files outside the app directory", func() {
				builder := builders[0]
				mask := masks.GetMask(builder)

				Expect(mask.Includes("other/file.txt")).To(BeFalse())
				Expect(mask.Includes("ap")).To(BeFalse())
			})
		})

		Context("with transitive copy", func() {
			var masks CopyMasks
			var firstBuilder, secondBuilder Builder

			BeforeEach(func() {
				builders := []Builder{
					{
						Alias:    "first",
						Pullspec: "builder-image",
						Copies: []Copy{
							{Source: []string{"/app"}, Dest: "/usr/app", Stage: "second"},
						},
					},
					{
						Alias:    "second",
						Pullspec: "builder-image",
						Copies: []Copy{
							{Source: []string{"/usr/app"}, Dest: "/usr/app", Stage: FinalStage},
						},
					},
				}
				masks = NewCopyMasks(builders)
				firstBuilder = builders[0]
				secondBuilder = builders[1]
			})

			It("should handle first builder correctly", func() {
				mask := masks.GetMask(firstBuilder)

				Expect(mask.Includes("app/file.txt")).To(BeTrue())
				Expect(mask.Includes("app")).To(BeTrue())
				Expect(mask.Includes("app/subdir/file.txt")).To(BeTrue())
				Expect(mask.Includes("other/file.txt")).To(BeFalse())
				Expect(mask.Includes("ap")).To(BeFalse())
			})

			It("should handle second builder correctly", func() {
				mask := masks.GetMask(secondBuilder)

				Expect(mask.Includes("usr/app/file.txt")).To(BeTrue())
				Expect(mask.Includes("usr/app/subdir/file.txt")).To(BeTrue())
				Expect(mask.Includes("ap")).To(BeFalse())
			})
		})

		Context("with transitive and final copy mix", func() {
			var masks CopyMasks
			var firstBuilder Builder

			BeforeEach(func() {
				builders := []Builder{
					{
						Alias:    "first",
						Pullspec: "builder-image",
						Copies: []Copy{
							{Source: []string{"/app"}, Dest: "/usr/app", Stage: "second"},
							{Source: []string{"/lib"}, Dest: "/app/lib", Stage: FinalStage},
						},
					},
					{
						Alias:    "second",
						Pullspec: "builder-image",
						Copies: []Copy{
							{Source: []string{"/usr/app"}, Dest: "/usr/app", Stage: FinalStage},
						},
					},
				}
				masks = NewCopyMasks(builders)
				firstBuilder = builders[0]
			})

			It("should include files from both app and lib directories", func() {
				mask := masks.GetMask(firstBuilder)

				Expect(mask.Includes("app/file.txt")).To(BeTrue())
				Expect(mask.Includes("lib/lib.h")).To(BeTrue())
			})
		})

		Context("with root path as source", func() {
			var masks CopyMasks
			var testBuilder Builder

			BeforeEach(func() {
				builders := []Builder{
					{
						Alias:    "test",
						Pullspec: "test-image",
						Copies: []Copy{
							{Source: []string{"/"}, Dest: "/copy"},
						},
					},
				}
				masks = NewCopyMasks(builders)
				testBuilder = builders[0]
			})

			It("should include any path when source is root", func() {
				mask := masks.GetMask(testBuilder)

				Expect(mask.Includes("anything")).To(BeTrue())
			})
		})

		Context("when path exactly matches source", func() {
			var masks CopyMasks
			var testBuilder Builder

			BeforeEach(func() {
				builders := []Builder{
					{
						Alias:    "test",
						Pullspec: "test-image",
						Copies: []Copy{
							{Source: []string{"/exact/path"}, Dest: "/dest"},
						},
					},
				}
				masks = NewCopyMasks(builders)
				testBuilder = builders[0]
			})

			It("should include the exact path", func() {
				mask := masks.GetMask(testBuilder)

				Expect(mask.Includes("exact/path")).To(BeTrue())
			})
		})
	})
})

var _ = Describe("NewCopyMasks", func() {
	Context("when given empty builders slice", func() {
		It("should return empty mask for any builder", func() {
			result := NewCopyMasks([]Builder{})

			dummyBuilder := Builder{Alias: "nonexistent"}
			mask := result.GetMask(dummyBuilder)
			Expect(mask.GetSources()).To(HaveLen(0))
		})
	})
})
