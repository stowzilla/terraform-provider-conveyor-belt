class User
  attr_accessor :id, :name, :email

  def initialize(id:, name:, email:)
    @id = id
    @name = name
    @email = email
  end

  def to_hash
    {
      id: @id,
      name: @name,
      email: @email
    }
  end
end